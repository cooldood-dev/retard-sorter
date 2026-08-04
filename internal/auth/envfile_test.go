// File: internal/auth/envfile_test.go

package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// resetSecrets unsets every var LoadEnvSecrets may touch so tests start clean.
func resetSecrets(t *testing.T) {
	t.Helper()
	for _, k := range []string{"SUPABASE_URL", "SUPABASE_SERVICE_KEY", "SUPABASE_SERVICE_ROLE_KEY"} {
		t.Setenv(k, "")
	}
}

func TestApplyEnvLines(t *testing.T) {
	os.Unsetenv("TEST_VAR_ONE")
	os.Unsetenv("TEST_VAR_TWO")
	applyEnvLines(`
# a comment
TEST_VAR_ONE=hello
TEST_VAR_TWO="quoted value"

no-equals-line
`)
	if os.Getenv("TEST_VAR_ONE") != "hello" {
		t.Fatalf("TEST_VAR_ONE = %q", os.Getenv("TEST_VAR_ONE"))
	}
	if os.Getenv("TEST_VAR_TWO") != "quoted value" {
		t.Fatalf("TEST_VAR_TWO = %q", os.Getenv("TEST_VAR_TWO"))
	}
	os.Unsetenv("TEST_VAR_ONE")
	os.Unsetenv("TEST_VAR_TWO")
}

func TestApplyEnvLinesDoesNotOverwriteExisting(t *testing.T) {
	t.Setenv("TEST_VAR_KEEP", "real-env")
	applyEnvLines("TEST_VAR_KEEP=from-file\n")
	if os.Getenv("TEST_VAR_KEEP") != "real-env" {
		t.Fatalf("env var was overwritten by file: %q", os.Getenv("TEST_VAR_KEEP"))
	}
}

func TestLoadEnvSecretsFromPlaintextFile(t *testing.T) {
	resetSecrets(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, envFileName),
		[]byte("SUPABASE_URL=https://example.supabase.co\nSUPABASE_SERVICE_KEY=sk-plain\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	LoadEnvSecrets(dir)
	if os.Getenv("SUPABASE_URL") != "https://example.supabase.co" {
		t.Fatalf("SUPABASE_URL = %q", os.Getenv("SUPABASE_URL"))
	}
	if os.Getenv("SUPABASE_SERVICE_KEY") != "sk-plain" {
		t.Fatalf("SUPABASE_SERVICE_KEY = %q", os.Getenv("SUPABASE_SERVICE_KEY"))
	}
}

func TestLoadEnvSecretsBridgesRoleKey(t *testing.T) {
	resetSecrets(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, envFileName),
		[]byte("SUPABASE_SERVICE_ROLE_KEY=sk-role-legacy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	LoadEnvSecrets(dir)
	if os.Getenv("SUPABASE_SERVICE_KEY") != "sk-role-legacy" {
		t.Fatalf("bridge failed: SUPABASE_SERVICE_KEY = %q", os.Getenv("SUPABASE_SERVICE_KEY"))
	}
}

func TestLoadEnvSecretsEnvVarsWin(t *testing.T) {
	t.Setenv("SUPABASE_URL", "https://real-env.supabase.co")
	t.Setenv("SUPABASE_SERVICE_KEY", "sk-env")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, envFileName),
		[]byte("SUPABASE_URL=https://file.supabase.co\nSUPABASE_SERVICE_KEY=sk-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	LoadEnvSecrets(dir)
	if os.Getenv("SUPABASE_URL") != "https://real-env.supabase.co" {
		t.Fatalf("env var lost: %q", os.Getenv("SUPABASE_URL"))
	}
	if os.Getenv("SUPABASE_SERVICE_KEY") != "sk-env" {
		t.Fatalf("env var lost: %q", os.Getenv("SUPABASE_SERVICE_KEY"))
	}
}

// TestEncryptEnvFileRoundTrip exercises the full encrypt/decrypt path with
// the real machine key: encrypt a plaintext file, then LoadEnvSecrets must
// recover the values from the encrypted form after the plaintext is gone.
func TestEncryptEnvFileRoundTrip(t *testing.T) {
	resetSecrets(t)
	dir := t.TempDir()
	plain := filepath.Join(dir, envFileName)
	if err := os.WriteFile(plain,
		[]byte("SUPABASE_URL=https://example.supabase.co\nSUPABASE_SERVICE_ROLE_KEY=sk-encrypted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EncryptEnvFile(dir); err != nil {
		t.Skipf("EncryptEnvFile unavailable on this machine: %v", err)
	}
	if _, err := os.Stat(plain); !os.IsNotExist(err) {
		t.Fatal("plaintext not removed after encryption")
	}
	if _, err := os.Stat(filepath.Join(dir, encryptedEnvFileName)); err != nil {
		t.Fatalf("supabase.env.enc missing: %v", err)
	}
	LoadEnvSecrets(dir)
	if os.Getenv("SUPABASE_SERVICE_KEY") != "sk-encrypted" {
		t.Fatalf("decrypted key wrong: %q", os.Getenv("SUPABASE_SERVICE_KEY"))
	}
	if os.Getenv("SUPABASE_URL") != "https://example.supabase.co" {
		t.Fatalf("decrypted URL wrong: %q", os.Getenv("SUPABASE_URL"))
	}
}
