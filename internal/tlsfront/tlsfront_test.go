package tlsfront

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// selfSigned returns PEM for a certificate valid for domain until notAfter,
// with the key appended, in the shape `tailscale cert` prints.
func selfSigned(t *testing.T, domain string, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	var out []byte
	out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	out = append(out, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})...)
	return out
}

const statusJSON = `{"Self":{"DNSName":"box.tail1234.ts.net."},"CertDomains":["box.tail1234.ts.net"]}`

// fakeTailscale answers status and cert like the CLI. calls counts cert
// issuances so tests can see renewals.
func fakeTailscale(t *testing.T, pemOut []byte, calls *int, certErr error) RunFunc {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch args[0] {
		case "status":
			return []byte(statusJSON), nil
		case "cert":
			*calls++
			if certErr != nil {
				return nil, certErr
			}
			if args[len(args)-1] != "box.tail1234.ts.net" {
				t.Errorf("cert requested for %q", args[len(args)-1])
			}
			// The real CLI prints the pair twice when both go to stdout.
			return append(append([]byte{}, pemOut...), pemOut...), nil
		}
		return nil, errors.New("unexpected command")
	}
}

func TestOffReturnsNil(t *testing.T) {
	f, err := Load(context.Background(), Options{Mode: Off})
	if err != nil || f != nil {
		t.Fatalf("Off should yield nil, nil; got %v, %v", f, err)
	}
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"": Off, "off": Off, "tailscale": Tailscale, "TS": Tailscale, "file": File} {
		got, err := ParseMode(in)
		if err != nil || got != want {
			t.Errorf("ParseMode(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseMode("letsencrypt"); err == nil {
		t.Error("unknown mode should error")
	}
}

func TestTailscaleServesHTTP2(t *testing.T) {
	calls := 0
	pemOut := selfSigned(t, "box.tail1234.ts.net", time.Now().Add(80*24*time.Hour))
	f, err := Load(context.Background(), Options{
		Mode:     Tailscale,
		CacheDir: t.TempDir(),
		Run:      fakeTailscale(t, pemOut, &calls, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.Host != "box.tail1234.ts.net" {
		t.Errorf("Host = %q", f.Host)
	}
	if calls != 1 {
		t.Errorf("want one issuance at load, got %d", calls)
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Proto))
	}))
	server.TLS = f.TLSConfig()
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, ServerName: "box.tail1234.ts.net"},
		ForceAttemptHTTP2: true,
	}}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Errorf("browser-style client negotiated %s, want HTTP/2", resp.Proto)
	}
	if got := resp.TLS.PeerCertificates[0].DNSNames; len(got) != 1 || got[0] != "box.tail1234.ts.net" {
		t.Errorf("served certificate for %v", got)
	}
}

func TestTailscaleFallsBackToCache(t *testing.T) {
	dir := t.TempDir()
	pemOut := selfSigned(t, "box.tail1234.ts.net", time.Now().Add(80*24*time.Hour))
	calls := 0
	if _, err := Load(context.Background(), Options{Mode: Tailscale, CacheDir: dir, Run: fakeTailscale(t, pemOut, &calls, nil)}); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(dir, "tls", "box.tail1234.ts.net.pem")
	if st, err := os.Stat(cache); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("cache not written owner-only: %v %v", st, err)
	}

	f, err := Load(context.Background(), Options{Mode: Tailscale, CacheDir: dir, Run: fakeTailscale(t, nil, &calls, errors.New("tailscaled down"))})
	if err != nil {
		t.Fatalf("cached certificate should carry a restart while tailscaled is down: %v", err)
	}
	cert, _ := f.source.getCertificate(nil)
	if cert == nil || cert.Leaf.DNSNames[0] != "box.tail1234.ts.net" {
		t.Error("cached certificate not served")
	}

	if _, err := Load(context.Background(), Options{Mode: Tailscale, CacheDir: t.TempDir(), Run: fakeTailscale(t, nil, &calls, errors.New("tailscaled down"))}); err == nil {
		t.Error("no cache and no tailscaled must fail loudly")
	}
}

func TestTailscaleRenewsInsideWindow(t *testing.T) {
	calls := 0
	now := time.Now()
	clock := func() time.Time { return now }
	pemOut := selfSigned(t, "box.tail1234.ts.net", now.Add(10*24*time.Hour))
	f, err := Load(context.Background(), Options{Mode: Tailscale, Run: fakeTailscale(t, pemOut, &calls, nil), Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	src := f.source.(*tailscaleSource)

	if _, err := src.getCertificate(nil); err != nil {
		t.Fatal(err)
	}
	src.mu.Lock()
	done := src.renewDone
	src.mu.Unlock()
	if done == nil {
		t.Fatal("certificate 10 days from expiry should trigger a renewal")
	}
	<-done
	if calls != 2 {
		t.Errorf("want a second issuance, got %d", calls)
	}

	// A second handshake straight after must not issue again.
	if _, err := src.getCertificate(nil); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("renewal should be rate limited, got %d issuances", calls)
	}
}

func TestStatusWithoutCertDomainsExplains(t *testing.T) {
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(`{"Self":{"DNSName":"box.tail1234.ts.net."},"CertDomains":[]}`), nil
	}
	_, err := Load(context.Background(), Options{Mode: Tailscale, Run: run})
	if err == nil || !strings.Contains(err.Error(), "HTTPS Certificates") {
		t.Errorf("want a hint about enabling HTTPS certificates, got %v", err)
	}
}

func TestMagicDNSNameStripsTrailingDot(t *testing.T) {
	if _, err := findTailscale(); err != nil {
		t.Skip("tailscale CLI not installed")
	}
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(statusJSON), nil
	}
	name, err := MagicDNSName(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if name != "box.tail1234.ts.net" {
		t.Errorf("MagicDNSName = %q, want box.tail1234.ts.net", name)
	}
}

func TestMagicDNSNameErrorsWithoutName(t *testing.T) {
	if _, err := findTailscale(); err != nil {
		t.Skip("tailscale CLI not installed")
	}
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(`{"Self":{"DNSName":""},"CertDomains":[]}`), nil
	}
	if _, err := MagicDNSName(context.Background(), run); err == nil {
		t.Error("empty DNSName must error")
	}
}

func TestFileAdapter(t *testing.T) {
	dir := t.TempDir()
	pemOut := selfSigned(t, "agy.example.com", time.Now().Add(time.Hour))
	certPath, keyPath := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	blocks := strings.SplitAfter(string(pemOut), "-----END CERTIFICATE-----\n")
	if err := os.WriteFile(certPath, []byte(blocks[0]), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte(blocks[1]), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := Load(context.Background(), Options{Mode: File, CertFile: certPath, KeyFile: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	if f.Host != "" {
		t.Errorf("file adapter should not dictate a host, got %q", f.Host)
	}
	cert, _ := f.source.getCertificate(nil)
	if cert == nil {
		t.Fatal("no certificate")
	}

	if _, err := Load(context.Background(), Options{Mode: File, CertFile: certPath}); err == nil {
		t.Error("missing key must error")
	}
}

func TestParsePEMDropsDuplicates(t *testing.T) {
	pemOut := selfSigned(t, "x", time.Now().Add(time.Hour))
	cert, err := parsePEM(append(append([]byte{}, pemOut...), pemOut...))
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.Certificate) != 1 {
		t.Errorf("want one certificate after dedupe, got %d", len(cert.Certificate))
	}
}
