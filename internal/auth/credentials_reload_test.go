package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A password set by the passwd command reaches an already-running server only if
// Verify re-reads the file, so this guards against the old password continuing to
// work after a rotation.
func TestVerifyPicksUpAPasswordSetByAnotherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")

	serving, _, err := LoadOrCreateCredentials(path, "first-password")
	if err != nil {
		t.Fatal(err)
	}
	if !serving.Verify("first-password") {
		t.Fatal("the password it was created with should verify")
	}

	rotating, err := LoadCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := rotating.Set("second-password"); err != nil {
		t.Fatal(err)
	}

	if serving.Verify("first-password") {
		t.Error("the rotated-away password still verifies")
	}
	if !serving.Verify("second-password") {
		t.Error("the newly set password does not verify")
	}
}

func TestSessionStorePicksUpRevokeAllFromAnotherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")

	serving, err := NewSessionStore(path, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	token, err := serving.Create("test-agent", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !serving.Validate(token) {
		t.Fatal("newly created session should validate")
	}

	// Simulate `agy-server passwd` opening a separate SessionStore and calling RevokeAll.
	cliStore, err := NewSessionStore(path, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// Ensure mtime advances even on filesystems with coarse timestamp resolution.
	future := time.Now().Add(2 * time.Second)
	if _, err := cliStore.RevokeAll(); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(path, future, future)

	if serving.Validate(token) {
		t.Error("revoked session still validates on the running server instance")
	}
	if got := serving.Count(); got != 0 {
		t.Errorf("serving.Count() = %d, want 0", got)
	}
}
