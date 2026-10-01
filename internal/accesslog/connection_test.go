package accesslog

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestsOnOneConnectionShareAnID(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf)
	server := httptest.NewUnstartedServer(logger.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("body"))
	})))
	server.Config.ConnContext = logger.ConnContext
	server.Config.ConnState = logger.ConnState
	server.Start()
	defer server.Close()
	client := server.Client()
	var connection string
	for i := 0; i < 2; i++ {
		response, err := client.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		id := response.Header.Get("X-Connection-ID")
		if id == "" {
			t.Fatal("connection ID missing")
		}
		if connection != "" && id != connection {
			t.Fatal("same keepalive connection got a different ID")
		}
		connection = id
	}
	contents := logContents(logger, &buf)
	if strings.Count(contents, "CONN OPEN") != 1 {
		t.Fatalf("connection opened more than once: %s", contents)
	}
	if strings.Count(contents, "conn="+connection) != 3 {
		t.Fatalf("requests do not correlate with the connection: %s", contents)
	}
}

type slowFlush struct {
	*httptest.ResponseRecorder
	started, release chan struct{}
}

func (w *slowFlush) FlushError() error {
	close(w.started)
	<-w.release
	return net.ErrClosed
}

func TestOpenRequestReportsBlockedFlushAndItsError(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf)
	logger.openAfter = 10 * time.Millisecond
	underlying := &slowFlush{ResponseRecorder: httptest.NewRecorder(), started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		logger.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.(http.Flusher).Flush()
		})).ServeHTTP(underlying, httptest.NewRequest("GET", "/", nil))
	}()
	<-underlying.started
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(logContents(logger, &buf), "OPEN") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	open := logContents(logger, &buf)
	close(underlying.release)
	<-done
	if !strings.Contains(open, "io=flush") || !strings.Contains(open, "busy=") {
		t.Fatalf("blocked flush not visible while request is open: %s", open)
	}
	completed := logContents(logger, &buf)
	if !strings.Contains(completed, "flush=") || !strings.Contains(completed, `io_error="`+net.ErrClosed.Error()+`"`) {
		t.Fatalf("flush duration/error missing: %s", completed)
	}
}
