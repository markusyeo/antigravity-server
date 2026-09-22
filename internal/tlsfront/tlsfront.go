// Package tlsfront terminates TLS on the public listener so browsers speak
// HTTP/2 to agy-server.
//
// This exists because of a browser limit, not for secrecy. Over plain HTTP a
// browser opens at most six connections to one host. Antigravity's web UI holds
// six long-lived streams per open conversation, so every other request queues
// in the browser until a stream drops, which is what a 25-second "blocked"
// phase in a HAR file looks like. HTTP/2 multiplexes everything over one
// connection, and browsers only negotiate it over TLS. Go's server enables
// HTTP/2 automatically once it serves TLS.
//
// Two adapters sit behind the seam: a certificate issued by the local Tailscale
// daemon for this node's MagicDNS name, trusted by every device on the tailnet,
// and a certificate and key supplied as files.
package tlsfront

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Mode selects the adapter.
type Mode string

const (
	// Off serves plain HTTP, as before.
	Off Mode = "off"
	// Tailscale asks the local tailscaled for a certificate.
	Tailscale Mode = "tailscale"
	// File loads a certificate and key from disk.
	File Mode = "file"
)

// ParseMode accepts the spellings a user might type.
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "off", "none", "false", "0":
		return Off, nil
	case "tailscale", "ts":
		return Tailscale, nil
	case "file", "cert":
		return File, nil
	}
	return Off, fmt.Errorf("unknown TLS mode %q (want off, tailscale or file)", s)
}

// RunFunc executes a command and returns its stdout. It is a parameter so
// tests can stand in for the tailscale CLI.
type RunFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// Options configures Load.
type Options struct {
	Mode Mode
	// CertFile and KeyFile are used by the File adapter.
	CertFile, KeyFile string
	// CacheDir is where the Tailscale adapter keeps the last certificate it was
	// issued, so a restart while tailscaled is unreachable still comes up.
	CacheDir string
	// Run overrides command execution. Nil means exec.CommandContext.
	Run RunFunc
	// Now overrides the clock for renewal decisions.
	Now func() time.Time
}

// Front is a loaded TLS configuration and the host name its certificate is for.
type Front struct {
	// Host is the name browsers must use for the certificate to validate. Empty
	// for the File adapter, whose certificate the user chose.
	Host   string
	source source
}

type source interface {
	getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error)
}

// Load resolves the adapter for opts.Mode. It returns nil for Off so callers
// can keep a single code path: a nil *Front means serve plain HTTP.
func Load(ctx context.Context, opts Options) (*Front, error) {
	if opts.Run == nil {
		opts.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	switch opts.Mode {
	case "", Off:
		return nil, nil
	case Tailscale:
		return loadTailscale(ctx, opts)
	case File:
		return loadFile(opts)
	}
	return nil, fmt.Errorf("unknown TLS mode %q", opts.Mode)
}

// TLSConfig is what http.Server needs. Leaving NextProtos empty lets Go add
// h2 itself when the server starts serving TLS.
func (f *Front) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: f.source.getCertificate,
	}
}

// --- File adapter -----------------------------------------------------------

type fileSource struct {
	certFile, keyFile string

	mu      sync.Mutex
	cert    *tls.Certificate
	modTime time.Time
	checked time.Time
	now     func() time.Time
}

func loadFile(opts Options) (*Front, error) {
	if opts.CertFile == "" || opts.KeyFile == "" {
		return nil, errors.New("TLS mode file needs both --tls-cert and --tls-key")
	}
	s := &fileSource{certFile: opts.CertFile, keyFile: opts.KeyFile, now: opts.Now}
	if err := s.reload(); err != nil {
		return nil, err
	}
	return &Front{source: s}, nil
}

func (s *fileSource) reload() error {
	cert, err := tls.LoadX509KeyPair(s.certFile, s.keyFile)
	if err != nil {
		return fmt.Errorf("load TLS certificate: %w", err)
	}
	s.cert = &cert
	if st, err := os.Stat(s.certFile); err == nil {
		s.modTime = st.ModTime()
	}
	return nil
}

// getCertificate picks up a replaced file without a restart, checking the
// modification time at most every ten seconds.
func (s *fileSource) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now := s.now(); now.Sub(s.checked) > 10*time.Second {
		s.checked = now
		if st, err := os.Stat(s.certFile); err == nil && !st.ModTime().Equal(s.modTime) {
			_ = s.reload()
		}
	}
	return s.cert, nil
}

// --- Tailscale adapter ------------------------------------------------------

// renewBefore is how close to expiry a renewal is attempted. Tailscale issues
// 90-day certificates and renews them itself once inside this window.
const renewBefore = 14 * 24 * time.Hour

// renewRetry spaces out renewal attempts when tailscaled keeps failing.
const renewRetry = time.Hour

type tailscaleSource struct {
	domain    string
	binary    string
	cachePath string
	run       RunFunc
	now       func() time.Time

	mu        sync.Mutex
	cert      *tls.Certificate
	notAfter  time.Time
	lastTry   time.Time
	renewing  bool
	renewDone chan struct{}
}

func loadTailscale(ctx context.Context, opts Options) (*Front, error) {
	binary, err := findTailscale()
	if err != nil {
		return nil, err
	}

	domain, err := certDomain(ctx, opts.Run, binary)
	if err != nil {
		return nil, err
	}

	s := &tailscaleSource{
		domain: domain,
		binary: binary,
		run:    opts.Run,
		now:    opts.Now,
	}
	if opts.CacheDir != "" {
		s.cachePath = filepath.Join(opts.CacheDir, "tls", domain+".pem")
	}

	if err := s.issue(ctx); err != nil {
		cached, cerr := s.loadCache()
		if cerr != nil {
			return nil, fmt.Errorf("tailscale cert: %w", err)
		}
		s.set(cached)
	}

	return &Front{Host: domain, source: s}, nil
}

// findTailscale locates the CLI. The macOS app bundles it where PATH rarely
// points.
func findTailscale() (string, error) {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p, nil
	}
	candidates := []string{
		"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
		"/usr/bin/tailscale",
		"/usr/local/bin/tailscale",
	}
	if runtime.GOOS == "windows" {
		candidates = []string{`C:\Program Files\Tailscale\tailscale.exe`}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", errors.New("tailscale CLI not found; install Tailscale or use --tls file")
}

// certDomain asks tailscaled which names it can issue for. The list is empty
// until HTTPS certificates are switched on in the tailnet's DNS settings.
func certDomain(ctx context.Context, run RunFunc, binary string) (string, error) {
	out, err := run(ctx, binary, "status", "--json")
	if err != nil {
		return "", fmt.Errorf("tailscale status: %w (is Tailscale running and signed in?)", err)
	}
	var status struct {
		CertDomains []string `json:"CertDomains"`
		Self        struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(out, &status); err != nil {
		return "", fmt.Errorf("parse tailscale status: %w", err)
	}
	if len(status.CertDomains) == 0 {
		name := strings.TrimSuffix(status.Self.DNSName, ".")
		return "", fmt.Errorf("tailscale cannot issue certificates for %s; enable MagicDNS and HTTPS Certificates under DNS in the Tailscale admin console", name)
	}
	return status.CertDomains[0], nil
}

// issue asks tailscaled for a certificate. Output goes to stdout because the
// sandboxed macOS app cannot write outside its own container.
func (s *tailscaleSource) issue(ctx context.Context) error {
	out, err := s.run(ctx, s.binary, "cert", "--cert-file", "-", "--key-file", "-", s.domain)
	if err != nil {
		return err
	}
	cert, err := parsePEM(out)
	if err != nil {
		return err
	}
	s.set(cert)
	s.saveCache(out)
	return nil
}

func (s *tailscaleSource) set(cert *tls.Certificate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cert = cert
	s.notAfter = cert.Leaf.NotAfter
}

// getCertificate serves the current certificate and kicks off a renewal in the
// background once inside the renewal window, so no handshake waits on
// tailscaled.
func (s *tailscaleSource) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.Lock()
	cert := s.cert
	now := s.now()
	due := now.After(s.notAfter.Add(-renewBefore)) && now.Sub(s.lastTry) > renewRetry && !s.renewing
	if due {
		s.renewing = true
		s.lastTry = now
		s.renewDone = make(chan struct{})
	}
	s.mu.Unlock()

	if due {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			_ = s.issue(ctx)
			s.mu.Lock()
			s.renewing = false
			close(s.renewDone)
			s.mu.Unlock()
		}()
	}
	return cert, nil
}

func (s *tailscaleSource) saveCache(pemBytes []byte) {
	if s.cachePath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.cachePath), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(s.cachePath, pemBytes, 0o600)
}

func (s *tailscaleSource) loadCache() (*tls.Certificate, error) {
	if s.cachePath == "" {
		return nil, errors.New("no cached certificate")
	}
	data, err := os.ReadFile(s.cachePath)
	if err != nil {
		return nil, err
	}
	cert, err := parsePEM(data)
	if err != nil {
		return nil, err
	}
	if s.now().After(cert.Leaf.NotAfter) {
		return nil, errors.New("cached certificate has expired")
	}
	return cert, nil
}

// parsePEM builds a certificate from a blob holding the chain and the key in
// any order. The tailscale CLI, asked for both on stdout, prints each twice,
// so duplicate blocks are dropped.
func parsePEM(data []byte) (*tls.Certificate, error) {
	var certs, keys [][]byte
	seen := map[string]bool{}
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		key := block.Type + string(block.Bytes)
		if seen[key] {
			continue
		}
		seen[key] = true
		encoded := pem.EncodeToMemory(block)
		switch {
		case block.Type == "CERTIFICATE":
			certs = append(certs, encoded)
		case strings.Contains(block.Type, "PRIVATE KEY"):
			keys = append(keys, encoded)
		}
	}
	if len(certs) == 0 || len(keys) == 0 {
		return nil, errors.New("certificate output held no certificate and key pair")
	}
	cert, err := tls.X509KeyPair(joinPEM(certs), keys[0])
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	return &cert, nil
}

func joinPEM(blocks [][]byte) []byte {
	var out []byte
	for _, b := range blocks {
		out = append(out, b...)
	}
	return out
}
