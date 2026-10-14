package signin

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/AFSlayer/antigravity-server/internal/lsproc"
)

func TestCoordinatorCompleteClearsPendingSessionOnDeadCallback(t *testing.T) {
	// Allocate and immediately close a local port so callback fetch fails with connection refused.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadPort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	// Start a mock language server TLS endpoint for Login and GetAuthStatus RPCs.
	var loginCalls int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/exa.language_server_pb.LanguageServerService/Login":
			loginCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case "/exa.language_server_pb.LanguageServerService/GetAuthStatus":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"authResult":{"hasValidAuth":false}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	lsPort := server.Listener.Addr().(*net.TCPAddr).Port
	inst := &lsproc.Instance{Port: lsPort, CSRFToken: "test-token"}

	tmpDir := t.TempDir()
	urlFile := filepath.Join(tmpDir, "last-url")

	coord := New(inst, urlFile)

	// Pre-populate urlFile as if the browser shim captured a consent URL pointing to deadPort.
	firstConsentURL := "https://accounts.google.com/o/oauth2/v2/auth?redirect_uri=http%3A%2F%2Flocalhost%3A" + strconv.Itoa(deadPort) + "%2Foauth-callback"
	if err := os.WriteFile(urlFile, []byte(firstConsentURL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Because Begin removes urlFile first, write the URL asynchronously right after Begin starts waiting.
	go func() {
		for i := 0; i < 50; i++ {
			_ = os.WriteFile(urlFile, []byte(firstConsentURL+"\n"), 0o600)
		}
	}()

	gotURL, err := coord.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}
	if gotURL != firstConsentURL {
		t.Fatalf("got URL %q, want %q", gotURL, firstConsentURL)
	}

	// Attempt Complete with a valid code; callback to deadPort will fail.
	err = coord.Complete(context.Background(), "http://localhost:"+strconv.Itoa(deadPort)+"/oauth-callback?code=4/testcode")
	if err == nil {
		t.Fatal("expected Complete to fail when callback listener is dead")
	}
	if !strings.Contains(err.Error(), "could not hand the code to Antigravity") {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify that pending session was cleared so a subsequent Begin starts a fresh Login flow.
	coord.mu.Lock()
	stillPending := coord.pending != nil
	coord.mu.Unlock()
	if stillPending {
		t.Fatal("expected pending session to be cleared after callback failure, but it remained set")
	}
}

func TestNeedsSignInDistinguishesSignedOutFromRPCError(t *testing.T) {
	var failRPC bool
	var signedIn bool

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/exa.language_server_pb.LanguageServerService/GetAuthStatus" {
			if failRPC {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"authResult": map[string]any{
					"hasValidAuth": signedIn,
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	lsPort := server.Listener.Addr().(*net.TCPAddr).Port
	inst := &lsproc.Instance{Port: lsPort, CSRFToken: "test-token"}
	urlFile := filepath.Join(t.TempDir(), "last-url")

	// 1. When RPC fails (e.g. LS busy/starting), NeedsSignIn must be false even though SignedIn is false.
	failRPC = true
	c1 := New(inst, urlFile)
	if c1.SignedIn(context.Background()) {
		t.Fatal("expected SignedIn=false when RPC fails")
	}
	if c1.NeedsSignIn(context.Background()) {
		t.Fatal("expected NeedsSignIn=false when RPC fails (unknown auth state)")
	}

	// 2. When RPC succeeds with hasValidAuth=false, NeedsSignIn must be true.
	failRPC = false
	signedIn = false
	c2 := New(inst, urlFile)
	if c2.SignedIn(context.Background()) {
		t.Fatal("expected SignedIn=false when signed out")
	}
	if !c2.NeedsSignIn(context.Background()) {
		t.Fatal("expected NeedsSignIn=true when LS explicitly reports hasValidAuth=false")
	}

	// 3. When RPC succeeds with hasValidAuth=true, SignedIn=true and NeedsSignIn=false.
	signedIn = true
	c3 := New(inst, urlFile)
	if !c3.SignedIn(context.Background()) {
		t.Fatal("expected SignedIn=true when signed in")
	}
	if c3.NeedsSignIn(context.Background()) {
		t.Fatal("expected NeedsSignIn=false when signed in")
	}
}
