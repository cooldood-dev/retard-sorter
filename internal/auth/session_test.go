// File: internal/auth/session_test.go

package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sessionFileForTest(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.enc")
	t.Setenv("SESSION_FILE", path)
	return path
}

func TestSessionRoundTrip(t *testing.T) {
	sessionFileForTest(t)

	sess := Session{
		LicenseKey:  "KEY-ABC-123",
		ActivatedAt: time.Now().Add(-time.Minute),
	}
	if err := SaveSession(sess); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	got, err := LoadSession()
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if got.LicenseKey != sess.LicenseKey {
		t.Fatalf("LicenseKey mismatch: %q != %q", got.LicenseKey, sess.LicenseKey)
	}
	if !got.ActivatedAt.Equal(sess.ActivatedAt) {
		t.Fatalf("ActivatedAt mismatch: %v != %v", got.ActivatedAt, sess.ActivatedAt)
	}
}

func TestSessionMissingIsErrNoSession(t *testing.T) {
	sessionFileForTest(t)
	if _, err := LoadSession(); err != ErrNoSession {
		t.Fatalf("want ErrNoSession, got %v", err)
	}
}

func TestSessionCorruptFileIsErrNoSession(t *testing.T) {
	path := sessionFileForTest(t)
	if err := os.WriteFile(path, []byte("not a ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSession(); err != ErrNoSession {
		t.Fatalf("want ErrNoSession, got %v", err)
	}
}

func TestSessionExpiredIsErrNoSession(t *testing.T) {
	// No token TTL anymore: a session with a license key is valid
	// regardless of ActivatedAt — the revalidator decides validity.
	sessionFileForTest(t)
	if err := SaveSession(Session{
		LicenseKey:  "KEY-OLD",
		ActivatedAt: time.Now().Add(-24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSession(); err != nil {
		t.Fatalf("old session must still load (no TTL): %v", err)
	}
}

func TestSaveSessionRejectsEmptyLicenseKey(t *testing.T) {
	sessionFileForTest(t)
	err := SaveSession(Session{})
	if err == nil {
		t.Fatal("want error for empty license key")
	}
}

func TestLoadSessionRejectsKeylessSession(t *testing.T) {
	path := sessionFileForTest(t)
	// Craft a validly-encrypted blob whose plaintext has no license key —
	// the old token-era format. Must be treated as no session.
	key, err := sessionKey()
	if err != nil {
		t.Skipf("sessionKey unavailable: %v", err)
	}
	var key32 [32]byte
	copy(key32[:], key)
	sealed, err := encrypt(key32, []byte(`{"license_key":"","activated_at":"2026-01-01T00:00:00Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, sealed, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSession(); err != ErrNoSession {
		t.Fatalf("want ErrNoSession for keyless session, got %v", err)
	}
}

func TestClearSessionRemovesFile(t *testing.T) {
	path := sessionFileForTest(t)
	if err := SaveSession(Session{LicenseKey: "K"}); err != nil {
		t.Fatal(err)
	}
	if err := ClearSession(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("session file still present: %v", err)
	}
}
