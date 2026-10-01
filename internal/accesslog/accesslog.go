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
	"context"
	"fmt"
	"io"
	"net"
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
	mu          sync.Mutex
	w           io.Writer
	now         func() time.Time
	openAfter   time.Duration
	inflight    atomic.Int64
	sequence    atomic.Uint64
	connections sync.Map
}

type connectionKey struct{}

func (l *Logger) ConnContext(ctx context.Context, conn net.Conn) context.Context {
	id := fmt.Sprintf("c%x-%x", l.now().UnixMilli(), l.sequence.Add(1))
	l.connections.Store(conn, id)
	l.printf("%s CONN OPEN conn=%s remote=%s", l.now().Format("15:04:05.000"), id, conn.RemoteAddr())
	return context.WithValue(ctx, connectionKey{}, id)
}

func (l *Logger) ConnState(conn net.Conn, state http.ConnState) {
	if state != http.StateClosed && state != http.StateHijacked {
		return
	}
	if id, ok := l.connections.LoadAndDelete(conn); ok {
		l.printf("%s CONN %s conn=%s", l.now().Format("15:04:05.000"), state, id)
	}
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

		id := fmt.Sprintf("%x-%x", start.UnixMilli(), l.sequence.Add(1))
		w.Header().Set("X-Request-ID", id)
		connection, _ := r.Context().Value(connectionKey{}).(string)
		if connection != "" {
			w.Header().Set("X-Connection-ID", connection)
		}
		rec := &recorder{ResponseWriter: w, status: http.StatusOK, start: start, now: l.now}
		name := shortName(r)

		done := make(chan struct{})
		timer := time.AfterFunc(l.openAfter, func() {
			select {
			case <-done:
			default:
				op, busy := rec.currentIO()
				l.printf("%s --- %8.3fs %10s %-6s %-40s inflight=%d %s id=%s bytes=%d conn=%s io=%s busy=%.3fs", start.Format("15:04:05.000"), l.openAfter.Seconds(), "OPEN", r.Method, name, l.inflight.Load(), r.Proto, id, rec.progress.Load(), connection, op, busy.Seconds())
			}
		})
		defer func() {
			close(done)
			timer.Stop()
			dur := l.now().Sub(start)
			l.printf("%s %3d %8.3fs %9dB %-6s %-40s inflight=%d %s id=%s headers=%.3fs first=%.3fs encoding=%s timing=%q conn=%s write=%.3fs flush=%.3fs io_error=%q", start.Format("15:04:05.000"), rec.status, dur.Seconds(), rec.bytes, r.Method, name, inflight, r.Proto, id, rec.headers.Seconds(), rec.first.Seconds(), rec.Header().Get("Content-Encoding"), rec.Header().Values("Server-Timing"), connection, float64(rec.writeNs.Load())/1e9, float64(rec.flushNs.Load())/1e9, rec.errorText())
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
	runes := []rune(p)
	if len(runes) > 40 {
		return string(runes[:37]) + "..."
	}
	return p
}

// recorder captures the status and byte count while passing everything else,
// including Flush and Hijack via Unwrap, straight through.
type recorder struct {
	http.ResponseWriter
	status           int
	bytes            int64
	wroteHeader      bool
	start            time.Time
	now              func() time.Time
	headers, first   time.Duration
	progress         atomic.Int64
	ioSince          atomic.Int64
	ioOp             atomic.Int32
	writeNs, flushNs atomic.Int64
	ioError          atomic.Value
}

func (r *recorder) currentIO() (string, time.Duration) {
	started := r.ioSince.Load()
	if started == 0 {
		return "idle", 0
	}
	op := "write"
	if r.ioOp.Load() == 2 {
		op = "flush"
	}
	return op, max(0, r.now().Sub(r.start)-time.Duration(started-1))
}

func (r *recorder) errorText() string {
	if value := r.ioError.Load(); value != nil {
		return value.(string)
	}
	return ""
}

func (r *recorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.status = status
		r.wroteHeader = true
		r.headers = r.now().Sub(r.start)
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(p []byte) (int, error) {
	if !r.wroteHeader {
		r.wroteHeader = true
		r.headers = r.now().Sub(r.start)
	}
	if len(p) > 0 && r.bytes == 0 {
		r.first = r.now().Sub(r.start)
	}
	started := r.now()
	r.ioOp.Store(1)
	r.ioSince.Store(started.Sub(r.start).Nanoseconds() + 1)
	n, err := r.ResponseWriter.Write(p)
	r.writeNs.Add(r.now().Sub(started).Nanoseconds())
	r.ioSince.Store(0)
	if err != nil {
		r.ioError.Store(err.Error())
	}
	r.bytes += int64(n)
	r.progress.Add(int64(n))
	return n, err
}

func (r *recorder) Flush() {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	started := r.now()
	r.ioOp.Store(2)
	r.ioSince.Store(started.Sub(r.start).Nanoseconds() + 1)
	err := http.NewResponseController(r.ResponseWriter).Flush()
	r.flushNs.Add(r.now().Sub(started).Nanoseconds())
	r.ioSince.Store(0)
	if err != nil && err != http.ErrNotSupported {
		r.ioError.Store(err.Error())
	}
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
