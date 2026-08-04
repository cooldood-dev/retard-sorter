// File: internal/auth/revalidate_test.go

package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"retard-sorter/internal/supabase"
)

func saveSessionWithLicense(t *testing.T, key string) {
	t.Helper()
	sessionFileForTest(t)
	if err := SaveSession(Session{
		LicenseKey:  key,
		ActivatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
}

// verdictServer returns a client pointed at a fake PostgREST that always
// answers activate_license with the given verdict.
func verdictServer(t *testing.T, verdict string) *supabase.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := json.Marshal(verdict)
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return supabase.NewForTest(srv.URL, "test-key")
}

func TestRevalidateDefinitiveVerdictsInvalidate(t *testing.T) {
	for _, verdict := range []string{"hwid_mismatch", "revoked", "invalid_key"} {
		t.Run(verdict, func(t *testing.T) {
			saveSessionWithLicense(t, "KEY-1")
			sb := verdictServer(t, verdict)

			var got string
			revalidateOnce(t.Context(), sb, func(reason string) { got = reason })
			if got == "" {
				t.Fatal("onInvalid not called for definitive verdict")
			}
			if _, err := LoadSession(); err != ErrNoSession {
				t.Fatalf("session not cleared: want ErrNoSession, got %v", err)
			}
		})
	}
}

func TestRevalidateValidVerdictKeepsSession(t *testing.T) {
	saveSessionWithLicense(t, "KEY-2")
	sb := verdictServer(t, "ok")

	invalidated := false
	revalidateOnce(t.Context(), sb, func(string) { invalidated = true })
	if invalidated {
		t.Fatal("onInvalid called for valid verdict")
	}
	if _, err := LoadSession(); err != nil {
		t.Fatalf("session lost for valid verdict: %v", err)
	}
}

func TestRevalidateOfflineKeepsSession(t *testing.T) {
	// httptest server whose handler never returns (request hangs until the
	// client's 10s timeout) would be slow; instead close the server
	// immediately so every call hits a transport error.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	sb := supabase.NewForTest(srv.URL, "test-key")
	srv.Close()

	saveSessionWithLicense(t, "KEY-3")
	invalidated := false
	revalidateOnce(t.Context(), sb, func(string) { invalidated = true })
	if invalidated {
		t.Fatal("onInvalid called while offline")
	}
	if _, err := LoadSession(); err != nil {
		t.Fatal("offline must never clear the session")
	}
}

func TestRevalidateUnexpectedVerdictKeepsSession(t *testing.T) {
	saveSessionWithLicense(t, "KEY-4")
	sb := verdictServer(t, "invalid_hwid")

	invalidated := false
	revalidateOnce(t.Context(), sb, func(string) { invalidated = true })
	if invalidated {
		t.Fatal("onInvalid called for unexpected verdict")
	}
	if _, err := LoadSession(); err != nil {
		t.Fatal("session lost for unexpected verdict")
	}
}
