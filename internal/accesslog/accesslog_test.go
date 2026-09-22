package accesslog

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLogsOneLinePerRequest(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)

	h := l.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("hello"))
	}))
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)

	resp, err := http.Post(server.URL+"/exa.language_server_pb.LanguageServerService/GetAllWorkflows?x=1", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	line := buf.String()
	for _, want := range []string{" 201 ", "5B", "POST", "GetAllWorkflows", "inflight=1", "HTTP/1.1"} {
		if !strings.Contains(line, want) {
			t.Errorf("log line %q missing %q", line, want)
		}
	}
	if strings.Contains(line, "?x=1") || strings.Contains(line, "exa.language_server_pb") {
		t.Errorf("RPC name should be reduced to the method, got %q", line)
	}
	if strings.Count(line, "\n") != 1 {
		t.Errorf("want exactly one line, got %q", line)
	}
}

func TestReportsLongLivedStreamsWhileOpen(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)
	l.openAfter = 20 * time.Millisecond

	release := make(chan struct{})
	var once sync.Once
	releaseOnce := func() { once.Do(func() { close(release) }) }
	t.Cleanup(releaseOnce)
	h := l.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/connect+json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-release
	}))
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)

	resp, err := http.Post(server.URL+"/exa.language_server_pb.LanguageServerService/StreamAgentStateUpdates", "application/connect+json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(buf.String(), "OPEN") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(buf.String(), "OPEN") || !strings.Contains(buf.String(), "StreamAgentStateUpdates") {
		t.Fatalf("stream was not reported while open: %q", buf.String())
	}

	releaseOnce()
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	deadline = time.Now().Add(2 * time.Second)
	for strings.Count(buf.String(), "\n") < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(buf.String(), " 200 ") {
		t.Errorf("want a completion line after the stream closed: %q", buf.String())
	}
}

func TestInflightCountsConcurrentRequests(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)

	release := make(chan struct{})
	h := l.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hold" {
			<-release
		}
	}))
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)

	held := make(chan struct{})
	go func() {
		defer close(held)
		if resp, err := http.Get(server.URL + "/hold"); err == nil {
			_, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for l.inflight.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	resp, err := http.Get(server.URL + "/quick")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	close(release)
	<-held

	if !strings.Contains(buf.String(), "/quick") || !strings.Contains(buf.String(), "inflight=2") {
		t.Errorf("quick request should see the held one in flight: %q", buf.String())
	}
}
