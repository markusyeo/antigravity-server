package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWarmBundleReusesRepresentationsAndRefreshesChangedUpstream(t *testing.T) {
	var revision atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
			t.Error("upstream received a validator for patched content")
		}
		w.Header().Set("Content-Type", "text/javascript")
		w.Header().Set("ETag", `"original"`)
		_, _ = fmt.Fprintf(w, "%s\n/* revision %d */", bigBundle(), revision.Load())
	}))
	defer upstream.Close()
	p, err := New(Options{TargetPort: upstreamPort(t, upstream)})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.WarmBundle(context.Background()); err != nil {
		t.Fatal(err)
	}
	prepared := p.bundle
	front := httptest.NewServer(p.Handler())
	defer front.Close()
	head, err := http.Head(front.URL + "/main.js")
	if err != nil {
		t.Fatal(err)
	}
	head.Body.Close()
	if p.bundle != prepared {
		t.Fatal("HEAD request replaced the prepared bundle with an empty body")
	}
	fetch := func(encoding string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, front.URL+"/main.js", nil)
		req.Header.Set("Accept-Encoding", encoding)
		req.Header.Set("If-None-Match", `"original"`)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := fetch("gzip")
	content := gunzip(t, resp.Body)
	resp.Body.Close()
	if p.bundle != prepared {
		t.Fatal("unchanged bundle was rebuilt")
	}
	if resp.ContentLength != int64(len(prepared.gzip)) {
		t.Fatal("incorrect compressed length")
	}
	if resp.Header.Get("ETag") != "" {
		t.Fatal("upstream validator survived rewriting")
	}
	if !strings.Contains(content, "window.location.origin") {
		t.Fatal("warmup did not patch the bundle")
	}
	resp = fetch("identity")
	identity := body(t, resp)
	if identity != content || resp.Header.Get("Content-Encoding") != "" {
		t.Fatal("identity and compressed representations disagree")
	}
	if !strings.Contains(resp.Header.Get("Vary"), "Accept-Encoding") {
		t.Fatal("identity representation is missing Vary")
	}
	revision.Store(1)
	resp = fetch("gzip")
	refreshed := gunzip(t, resp.Body)
	resp.Body.Close()
	if p.bundle == prepared || !strings.Contains(refreshed, "revision 1") {
		t.Fatal("upstream change returned a stale bundle")
	}
}

func TestGzipNegotiationHonorsExplicitRefusal(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   bool
	}{
		{"gzip;q=0, *;q=1", false}, {"*;q=1, gzip;q=0", false},
		{"gzip; q=0.5", true}, {"GZIP", true}, {"br, *;q=0.2", true},
		{"gzip;q=bogus", false}, {"gzip;q=2", false}, {"gzip;q=-1", false},
	} {
		t.Run(tc.header, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Accept-Encoding", tc.header)
			if got := acceptsGzip(req); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
