// Package accesslog records one line per request with the numbers that
// explain a slow page: how long it took, how many requests were in flight
// when it started, and which HTTP version carried it.
//
// The in-flight count and protocol are there because of the browser's
// six-connection limit on HTTP/1.1. Long-lived streams hold those slots, and
// once six are open every other request queues in the browser, where no
// server-side log can see it. A request that has been open for a while is
// logged once as OPEN so the streams holding the slots are visible while the
// stall is happening, not only after they close.
package accesslog

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultOpenAfter is how long a request may stay open before it is reported
// as a long-lived stream.
const DefaultOpenAfter = 5 * time.Second

// Logger writes access lines to one destination.
type Logger struct {
	mu        sync.Mutex
	w         io.Writer
	now       func() time.Time
	openAfter time.Duration
	inflight  atomic.Int64
}

// New returns a Logger writing to w.
func New(w io.Writer) *Logger {
	return &Logger{w: w, now: time.Now, openAfter: DefaultOpenAfter}
}

// Wrap returns a handler that logs every request served by next.
func (l *Logger) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := l.now()
		inflight := l.inflight.Add(1)
		defer l.inflight.Add(-1)

		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		name := shortName(r)

		done := make(chan struct{})
		timer := time.AfterFunc(l.openAfter, func() {
			select {
			case <-done:
			default:
				l.printf("%s OPEN  %5.0fs  %-6s %-40s inflight=%d %s", start.Format("15:04:05.000"), l.openAfter.Seconds(), r.Method, name, l.inflight.Load(), r.Proto)
			}
		})
		defer func() {
			close(done)
			timer.Stop()
			dur := l.now().Sub(start)
			l.printf("%s %3d %8.3fs %9dB %-6s %-40s inflight=%d %s", start.Format("15:04:05.000"), rec.status, dur.Seconds(), rec.bytes, r.Method, name, inflight, r.Proto)
		}()

		next.ServeHTTP(rec, r)
	})
}

func (l *Logger) printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, format+"\n", args...)
}

// shortName reduces a URL to what a reader scanning the log needs: the RPC
// method for language server calls, the path otherwise. Query strings are
// dropped so cache keys and IDs do not widen the column.
func shortName(r *http.Request) string {
	p := r.URL.Path
	if i := strings.LastIndex(p, "LanguageServerService/"); i >= 0 {
		return p[i+len("LanguageServerService/"):]
	}
	if len(p) > 40 {
		return p[:37] + "..."
	}
	return p
}

// recorder captures the status and byte count while passing everything else,
// including Flush and Hijack via Unwrap, straight through.
type recorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (r *recorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.status = status
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(p []byte) (int, error) {
	r.wroteHeader = true
	n, err := r.ResponseWriter.Write(p)
	r.bytes += int64(n)
	return n, err
}

func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
