package proxy

import (
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Wire encoding lives here rather than in a front reverse proxy because most
// front doors (Tailscale, LAN, the desktop companion) have none. The language
// server gzips unary RPC replies when the browser asks, but leaves streamed
// Connect responses and anything we patch as identity. Opening a long thread
// streams a snapshot of tens of megabytes, and an uncompressed patched bundle
// is 9 MB; both shrink roughly tenfold under gzip.
//
// Every write is flushed through the gzip stream, so token-by-token agent
// output keeps arriving as it does today. The cost is a slightly worse ratio
// than a buffered gzip, which is irrelevant next to identity.

// minCompressBytes skips gzip for tiny bodies where the header overhead and
// an extra syscall buy nothing. Bodies without a Content-Length are always
// compressed, because those are the streams.
const minCompressBytes = 1024

var gzipPool = sync.Pool{
	New: func() any {
		w, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed)
		return w
	},
}

// compressible reports whether a Content-Type is worth gzipping. Anything not
// listed passes through, so an unknown binary type is never made larger.
func compressible(contentType string) bool {
	ct := strings.ToLower(contentType)
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch {
	case strings.HasPrefix(ct, "text/"),
		strings.HasPrefix(ct, "application/json"),
		strings.HasPrefix(ct, "application/javascript"),
		strings.HasPrefix(ct, "application/x-javascript"),
		strings.HasPrefix(ct, "application/connect+"),
		strings.HasPrefix(ct, "application/grpc-web"),
		strings.HasPrefix(ct, "application/proto"),
		strings.HasPrefix(ct, "application/xml"),
		ct == "image/svg+xml",
		ct == "application/wasm":
		return true
	}
	return false
}

func acceptsGzip(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc := strings.TrimSpace(part)
		if i := strings.IndexByte(enc, ';'); i >= 0 {
			if strings.Contains(enc[i:], "q=0") && !strings.Contains(enc[i:], "q=0.") {
				continue
			}
			enc = strings.TrimSpace(enc[:i])
		}
		if enc == "gzip" || enc == "*" {
			return true
		}
	}
	return false
}

// gzipHandler compresses responses the upstream left as identity when the
// client accepts gzip and the body is a compressible type.
func gzipHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

// gzipResponseWriter decides on the first WriteHeader or Write whether to
// compress, once the upstream headers are known.
type gzipResponseWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	decided     bool
	wroteHeader bool
}

func (g *gzipResponseWriter) decide(status int) {
	if g.decided {
		return
	}
	g.decided = true

	h := g.Header()
	if status < 200 || status >= 300 || status == http.StatusNoContent {
		return
	}
	if h.Get("Content-Encoding") != "" || !compressible(h.Get("Content-Type")) {
		return
	}
	if cl := h.Get("Content-Length"); cl != "" {
		if n, err := strconv.Atoi(cl); err == nil && n < minCompressBytes {
			return
		}
	}

	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	h.Add("Vary", "Accept-Encoding")

	gz := gzipPool.Get().(*gzip.Writer)
	gz.Reset(g.ResponseWriter)
	g.gz = gz
}

func (g *gzipResponseWriter) WriteHeader(status int) {
	if g.wroteHeader {
		return
	}
	g.wroteHeader = true
	g.decide(status)
	g.ResponseWriter.WriteHeader(status)
}

func (g *gzipResponseWriter) Write(p []byte) (int, error) {
	if !g.wroteHeader {
		if g.Header().Get("Content-Type") == "" {
			g.Header().Set("Content-Type", http.DetectContentType(p))
		}
		g.WriteHeader(http.StatusOK)
	}
	if g.gz == nil {
		return g.ResponseWriter.Write(p)
	}
	return g.gz.Write(p)
}

// Flush pushes buffered gzip output down before flushing the connection, so
// each upstream chunk reaches the client as soon as the proxy forwards it.
func (g *gzipResponseWriter) Flush() {
	if g.gz != nil {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach Hijack and friends on the
// underlying writer for WebSocket upgrades, which are never compressed.
func (g *gzipResponseWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipResponseWriter) close() {
	if g.gz == nil {
		return
	}
	_ = g.gz.Close()
	gzipPool.Put(g.gz)
	g.gz = nil
}
