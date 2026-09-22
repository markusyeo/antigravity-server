package proxy

import (
	"bufio"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AFSlayer/antigravity-server/internal/patches"
)

func gunzip(t *testing.T, r io.Reader) string {
	t.Helper()
	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// bigBundle pads the stub past minCompressBytes so the patched bundle is a
// compression candidate, as the real 9 MB one is.
func bigBundle() string {
	return stubBundle + strings.Repeat("/* padding to make the bundle worth compressing */\n", 100)
}

func TestProxyGzipsPatchedBundle(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/main.js", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "" {
			t.Errorf("proxy must strip Accept-Encoding for patched documents, got %q", r.Header.Get("Accept-Encoding"))
		}
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = w.Write([]byte(bigBundle()))
	})
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	front, _ := newTestProxy(t, server)

	resp := get(t, front.URL, "/main.js?agy=k1", "")
	if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("want gzip on the patched bundle, got %q", got)
	}
	if resp.Header.Get("Content-Length") != "" {
		t.Error("Content-Length must be dropped once the body is re-encoded")
	}
	if !strings.Contains(resp.Header.Get("Vary"), "Accept-Encoding") {
		t.Error("want Vary: Accept-Encoding")
	}
	out := gunzip(t, resp.Body)
	if !strings.Contains(out, "window.location.origin") {
		t.Error("gzipped bundle is not the patched one")
	}
}

func TestProxyLeavesIdentityWhenClientDoesNotAcceptGzip(t *testing.T) {
	front, _ := newTestProxy(t, upstream(t))

	req, _ := http.NewRequest(http.MethodGet, front.URL+"/main.js?agy=k1", nil)
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Errorf("client did not accept gzip, got Content-Encoding %q", got)
	}
	if !strings.Contains(body(t, resp), "window.location.origin") {
		t.Error("identity body should still be the patched bundle")
	}
}

func TestProxyDoesNotDoubleEncode(t *testing.T) {
	front, _ := newTestProxy(t, upstream(t))

	resp := get(t, front.URL, "/gzipped.html", "text/html")
	if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("want upstream gzip preserved, got %q", got)
	}
	if !strings.Contains(gunzip(t, resp.Body), "Jetski Web") {
		t.Error("upstream gzip body must pass through exactly once")
	}
}

func TestProxySkipsIncompressibleTypes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/icon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", "4096")
		_, _ = w.Write(make([]byte, 4096))
	})
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	front, _ := newTestProxy(t, server)

	resp := get(t, front.URL, "/icon.png", "")
	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Errorf("binary asset must not be gzipped, got %q", got)
	}
	if got := resp.Header.Get("Content-Length"); got != "4096" {
		t.Errorf("Content-Length should survive untouched, got %q", got)
	}
}

func TestProxySkipsTinyBodies(t *testing.T) {
	front, _ := newTestProxy(t, upstream(t))

	resp := get(t, front.URL, "/prism_bundle.js", "")
	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Errorf("tiny body should stay identity, got %q", got)
	}
}

// TestProxyGzipsConnectStreamPerFrame is the thread-open case: a
// server-streaming Connect RPC the language server sends as identity. Each
// frame must arrive gzipped and before the upstream emits the next one, or
// token streaming would stall behind the compressor.
func TestProxyGzipsConnectStreamPerFrame(t *testing.T) {
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/exa.language_server_pb.LanguageServerService/StreamAgentStateUpdates", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/connect+json")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = w.Write([]byte(`{"update":{"frame":1,"pad":"` + strings.Repeat("x", 2000) + `"}}` + "\n"))
		fl.Flush()
		select {
		case <-release:
		case <-time.After(5 * time.Second):
			t.Error("upstream never released; first frame did not reach the client independently")
		}
		_, _ = w.Write([]byte(`{"update":{"frame":2}}` + "\n"))
		fl.Flush()
	})
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	front, _ := newTestProxy(t, server)

	req, _ := http.NewRequest(http.MethodPost, front.URL+"/exa.language_server_pb.LanguageServerService/StreamAgentStateUpdates", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/connect+json")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("want streamed Connect response gzipped, got %q", got)
	}
	if resp.Header.Get("Content-Length") != "" {
		t.Error("a stream must not carry a Content-Length")
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewReader(gz)

	first := make(chan string, 1)
	go func() {
		line, _ := lines.ReadString('\n')
		first <- line
	}()
	select {
	case line := <-first:
		if !strings.Contains(line, `"frame":1`) {
			t.Fatalf("unexpected first frame %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first frame was held back until the stream ended; gzip is not flushing per frame")
	}
	close(release)

	second, _ := lines.ReadString('\n')
	if !strings.Contains(second, `"frame":2`) {
		t.Fatalf("unexpected second frame %q", second)
	}
}

func TestProxyErrorPageStaysIdentity(t *testing.T) {
	server := upstream(t)
	port := upstreamPort(t, server)
	server.Close()

	p, err := New(Options{TargetPort: port, Patch: patches.Options{}})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(p.Handler())
	t.Cleanup(front.Close)

	resp := get(t, front.URL, "/", "text/html")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Errorf("error responses should stay identity, got %q", got)
	}
	if !strings.Contains(body(t, resp), "not reachable") {
		t.Error("error body missing")
	}
}

func TestAcceptsGzip(t *testing.T) {
	cases := map[string]bool{
		"":                        false,
		"gzip":                    true,
		"gzip, deflate, br, zstd": true,
		"br;q=1.0, gzip;q=0.8":    true,
		"identity":                false,
		"gzip;q=0":                false,
		"*":                       true,
		"deflate":                 false,
	}
	for hdr, want := range cases {
		r, _ := http.NewRequest(http.MethodGet, "/", nil)
		if hdr != "" {
			r.Header.Set("Accept-Encoding", hdr)
		}
		if got := acceptsGzip(r); got != want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", hdr, got, want)
		}
	}
}
