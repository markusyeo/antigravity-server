// Package proxy forwards requests to the language server and rewrites the web
// bundle on the way back.
//
// Streaming matters here: agent responses arrive as long-lived chunked bodies, so
// the proxy flushes immediately and never buffers. Upstream compression is only
// declined for the two documents that get patched; whatever comes back as
// identity, patched or streamed, is gzipped on the way out (see encoding.go).
package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AFSlayer/antigravity-server/internal/patches"
)

// ReportFunc receives the patch outcome the first time each document is served.
type ReportFunc func(target patches.Target, report patches.Report)

// Options configures a Proxy.
type Options struct {
	TargetPort      int
	TargetCSRFToken string
	PublicURL       string
	Patch           patches.Options
	OnReport        ReportFunc
}

// Proxy is a patching reverse proxy in front of one language server.
type Proxy struct {
	handler        http.Handler
	opts           Options
	reported       sync.Map
	activeConns    atomic.Int64
	lastActivityNs atomic.Int64
	bundleMu       sync.RWMutex
	bundle         *bundleCache
	transport      http.RoundTripper
}

type bundleCache struct {
	digest         [32]byte
	etag           string
	identity, gzip []byte
}

type requestTimingKey struct{}
type requestTiming struct {
	start time.Time
	gzip  bool
}

// New builds a Proxy targeting the language server on opts.TargetPort.
func New(opts Options) (*Proxy, error) {
	target, err := url.Parse(fmt.Sprintf("https://127.0.0.1:%d", opts.TargetPort))
	if err != nil {
		return nil, err
	}

	p := &Proxy{opts: opts}
	p.touchActivity()

	rp := httputil.NewSingleHostReverseProxy(target)
	rp.FlushInterval = -1
	rp.Transport = &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		DisableCompression:  true,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
	}
	p.transport = rp.Transport

	host := target.Host
	base := rp.Director
	rp.Director = func(req *http.Request) {
		*req = *req.WithContext(context.WithValue(req.Context(), requestTimingKey{}, requestTiming{time.Now(), acceptsGzip(req)}))
		base(req)
		req.Host = host
		req.Header.Set("Origin", target.String())

		if p.opts.TargetCSRFToken != "" {
			req.Header.Set("x-codeium-csrf-token", p.opts.TargetCSRFToken)
		}

		if wantsPatch(req) {
			req.Header.Del("Accept-Encoding")
			req.Header.Del("If-None-Match")
			req.Header.Del("If-Modified-Since")
		}
	}

	rp.ModifyResponse = p.modifyResponse
	rp.ErrorHandler = errorHandler

	p.handler = gzipHandler(rp)
	return p, nil
}

// WarmBundle prepares the patched representations before the first browser arrives.
func (p *Proxy) WarmBundle(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://127.0.0.1:%d/main.js", p.opts.TargetPort), nil)
	if err != nil {
		return err
	}
	if p.opts.TargetCSRFToken != "" {
		req.Header.Set("x-codeium-csrf-token", p.opts.TargetCSRFToken)
	}
	resp, err := p.transport.RoundTrip(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bundle warmup returned %s", resp.Status)
	}
	return p.modifyResponse(resp)
}

// Handler returns the HTTP handler to mount, tracking in-flight streams and activity for idle detection.
func (p *Proxy) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.activeConns.Add(1)
		p.touchActivity()
		defer func() {
			p.activeConns.Add(-1)
			p.touchActivity()
		}()

		if isWebSocketUpgrade(r) && !p.sameOriginUpgrade(r) {
			http.Error(w, "cross-origin WebSocket upgrade rejected", http.StatusForbidden)
			return
		}

		if r.URL.Path == "/main.js" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			if b := p.cachedBundle(); b != nil {
				if etagMatches(r.Header.Get("If-None-Match"), b.etag) {
					setBundleValidators(w.Header(), b.etag)
					w.Header().Add("Vary", "Accept-Encoding")
					w.WriteHeader(http.StatusNotModified)
					return
				}
				if r.Method == http.MethodHead {
					setBundleValidators(w.Header(), b.etag)
					w.Header().Add("Vary", "Accept-Encoding")
					payload := b.identity
					if acceptsGzip(r) && len(payload) >= minCompressBytes {
						payload = b.gzip
						w.Header().Set("Content-Encoding", "gzip")
					}
					w.Header().Set("Content-Type", "application/javascript")
					w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
					w.WriteHeader(http.StatusOK)
					return
				}
			}
		}

		p.handler.ServeHTTP(w, r)
	})
}

func (p *Proxy) touchActivity() {
	p.lastActivityNs.Store(time.Now().UnixNano())
}

// ActiveConnections returns the number of currently active in-flight requests or streams.
func (p *Proxy) ActiveConnections() int64 {
	return p.activeConns.Load()
}

// LastActivity returns the timestamp of the most recent request initiation or completion.
func (p *Proxy) LastActivity() time.Time {
	ns := p.lastActivityNs.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// IsIdle returns true if there are zero active connections and no request activity has occurred
// for at least the specified threshold duration.
func (p *Proxy) IsIdle(threshold time.Duration) bool {
	if p.activeConns.Load() != 0 {
		return false
	}
	last := p.LastActivity()
	if last.IsZero() {
		return true
	}
	return time.Since(last) >= threshold
}

func wantsPatch(req *http.Request) bool {
	if req.URL.Path == "/main.js" {
		return true
	}
	return strings.Contains(req.Header.Get("Accept"), "text/html")
}

// targetFor decides whether a response should be patched. Encoded bodies are
// skipped rather than mangled, and skipping also suppresses a misleading
// "anchor not found" report.
func targetFor(resp *http.Response) (patches.Target, bool) {
	if resp.Request == nil || resp.StatusCode != http.StatusOK {
		return 0, false
	}
	if resp.Request.Method == http.MethodHead {
		return 0, false
	}
	if resp.Header.Get("Content-Encoding") != "" {
		return 0, false
	}

	if resp.Request.URL.Path == "/main.js" {
		return patches.MainJS, true
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		return patches.HTML, true
	}
	return 0, false
}

func (p *Proxy) modifyResponse(resp *http.Response) error {
	if resp.Request != nil {
		if timing, ok := resp.Request.Context().Value(requestTimingKey{}).(requestTiming); ok {
			resp.Header.Add("Server-Timing", fmt.Sprintf("upstream;dur=%.1f", float64(time.Since(timing.start).Microseconds())/1000))
		}
	}
	target, ok := targetFor(resp)
	if !ok {
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}

	started := time.Now()
	var patched []byte
	var tag string
	if target == patches.MainJS {
		digest := sha256.Sum256(body)
		p.bundleMu.Lock()
		if p.bundle == nil || p.bundle.digest != digest {
			identity, report := patches.Apply(target, body, p.opts.Patch)
			p.report(target, report)
			var compressed bytes.Buffer
			writer := gzip.NewWriter(&compressed)
			_, _ = writer.Write(identity)
			_ = writer.Close()
			p.bundle = &bundleCache{
				digest:   digest,
				etag:     bundleETag(identity),
				identity: identity,
				gzip:     compressed.Bytes(),
			}
		}
		patched = p.bundle.identity
		tag = p.bundle.etag
		resp.Header.Add("Vary", "Accept-Encoding")
		if timing, ok := resp.Request.Context().Value(requestTimingKey{}).(requestTiming); ok && timing.gzip && len(patched) >= minCompressBytes {
			patched = p.bundle.gzip
			resp.Header.Set("Content-Encoding", "gzip")
		}
		p.bundleMu.Unlock()
	} else {
		var report patches.Report
		patched, report = patches.Apply(target, body, p.opts.Patch)
		p.report(target, report)
	}
	resp.Header.Add("Server-Timing", fmt.Sprintf("patch;dur=%.1f", float64(time.Since(started).Microseconds())/1000))
	// Upstream validators describe the original document, not our representation.
	resp.Header.Del("ETag")
	resp.Header.Del("Last-Modified")

	resp.Body = io.NopCloser(bytes.NewReader(patched))
	resp.ContentLength = int64(len(patched))
	resp.Header.Set("Content-Length", strconv.Itoa(len(patched)))

	if target == patches.HTML {
		resp.Header.Set("Cache-Control", "no-store")
	} else {
		setBundleValidators(resp.Header, tag)
	}

	return nil
}

func (p *Proxy) cachedBundle() *bundleCache {
	p.bundleMu.RLock()
	defer p.bundleMu.RUnlock()
	return p.bundle
}

const bundleCacheControl = "private, no-cache"

func bundleETag(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

func setBundleValidators(h http.Header, tag string) {
	h.Set("Cache-Control", bundleCacheControl)
	h.Set("ETag", tag)
	h.Del("Last-Modified")
}

func etagMatches(header, tag string) bool {
	if header == "" || tag == "" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimPrefix(strings.TrimSpace(part), "W/")
		if part == "*" || part == tag {
			return true
		}
	}
	return false
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func (p *Proxy) sameOriginUpgrade(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		log.Printf("[proxy] rejecting WebSocket upgrade: malformed Origin %q", origin)
		return false
	}
	originHost := u.Hostname()
	originPort := u.Port()
	if originPort == "" {
		if strings.EqualFold(u.Scheme, "https") || strings.EqualFold(u.Scheme, "wss") {
			originPort = "443"
		} else if strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "ws") {
			originPort = "80"
		}
	}

	candidates := []string{
		r.Host,
		firstValue(r.Header.Get("X-Forwarded-Host")),
	}
	if p.opts.PublicURL != "" {
		if pu, err := url.Parse(p.opts.PublicURL); err == nil && pu.Host != "" {
			candidates = append(candidates, pu.Host)
		}
	}

	verifiable := false
	for _, h := range candidates {
		candHost, candPort := splitHostMaybePort(h)
		if candHost == "" {
			continue
		}
		if strings.EqualFold(candHost, originHost) {
			if candPort == "" || candPort == originPort {
				return true
			}
		}
		if !isLoopbackName(candHost) {
			verifiable = true
		}
	}

	if !verifiable {
		log.Printf("[proxy] warning: WebSocket upgrade from Origin %q accepted without Host verification (reverse proxy forwarded no external Host header)", origin)
		return true
	}

	log.Printf("[proxy] rejected cross-origin WebSocket upgrade: Origin=%q does not match Host=%q or X-Forwarded-Host=%q. Ensure your reverse proxy forwards Host (e.g. proxy_set_header Host $host;)", origin, r.Host, r.Header.Get("X-Forwarded-Host"))
	return false
}

func splitHostMaybePort(hostport string) (string, string) {
	if h, port, err := net.SplitHostPort(hostport); err == nil {
		return h, port
	}
	return strings.Trim(hostport, "[]"), ""
}

func firstValue(v string) string {
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

func isLoopbackName(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (p *Proxy) report(target patches.Target, report patches.Report) {
	if p.opts.OnReport == nil {
		return
	}
	if _, loaded := p.reported.LoadOrStore(target, true); loaded {
		return
	}
	p.opts.OnReport(target, report)
}

func errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		return
	}
	msg := err.Error()
	if strings.Contains(msg, "context canceled") || strings.Contains(msg, "client disconnected") {
		return
	}

	if r.Header.Get("x-grpc-web") != "" || strings.Contains(r.Header.Get("Content-Type"), "application/grpc-web") {
		w.Header().Set("Content-Type", "application/grpc-web+json")
		w.Header().Set("grpc-status", "14") // Unavailable
		w.Header().Set("grpc-message", "Antigravity language server is restarting")
		w.WriteHeader(http.StatusOK)
		return
	}

	w.WriteHeader(http.StatusBadGateway)
	_, _ = w.Write([]byte("Antigravity is not reachable. Is the language server still running?"))
}
