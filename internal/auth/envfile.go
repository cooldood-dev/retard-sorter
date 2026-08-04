// File: internal/auth/envfile.go
//
// supabase.env.enc — the owner's secrets encrypted at rest.
//
// The plain supabase.env contains the SUPABASE_SERVICE_KEY (service role),
// which grants full admin access to the Supabase project. Leaving it as
// plaintext next to the exe means anyone who copies the app folder steals
// the key. The encrypted form (supabase.env.enc) is sealed with AES-256-GCM
// under the machine-derived key, so it decrypts only on the owner's machine
// and is unreadable anywhere else.
//
// At startup main.go calls LoadEnvSecrets(exeDir), which decrypts
// supabase.env.enc (if present) and applies the values as environment
// variables — the supabase client still reads everything from the
// environment, exactly as its contract requires.

package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// envFileName is the plaintext legacy config (local dev; deleted after
// `-encrypt-env` runs).
const envFileName = "supabase.env"

// encryptedEnvFileName is the machine-bound encrypted secret store.
const encryptedEnvFileName = "supabase.env.enc"

// EncryptedEnvFileName exposes the encrypted config filename for CLI output.
func EncryptedEnvFileName() string {
	return encryptedEnvFileName
}

// LoadEnvSecrets loads Supabase secrets into the environment before the
// supabase client is constructed. Resolution order (highest wins):
//
//  1. Real environment variables (already set — never overwritten)
//  2. supabase.env.enc — machine-encrypted secrets (owner's machine)
//  3. supabase.env — plaintext legacy file (local dev)
//
// It also bridges the old SUPABASE_SERVICE_ROLE_KEY spelling to the new
// SUPABASE_SERVICE_KEY name the supabase client reads, so existing encrypted
// env files keep working without re-encryption.
func LoadEnvSecrets(exeDir string) {
	loadEncryptedEnvFile(exeDir)
	loadEnvFile(filepath.Join(exeDir, envFileName))
	if os.Getenv("SUPABASE_SERVICE_KEY") == "" {
		if v := os.Getenv("SUPABASE_SERVICE_ROLE_KEY"); v != "" {
			os.Setenv("SUPABASE_SERVICE_KEY", v)
		}
	}
}

// EncryptEnvFile reads the plaintext supabase.env next to the executable,
// encrypts its contents with the machine key, writes supabase.env.enc (0600)
// and removes the plaintext file. Owner-side helper: run
//
//	retard-sorter.exe -encrypt-env
//
// after creating/editing supabase.env.
func EncryptEnvFile(exeDir string) error {
	plainPath := filepath.Join(exeDir, envFileName)
	data, err := os.ReadFile(plainPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", envFileName, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return fmt.Errorf("%s is empty; nothing to encrypt", envFileName)
	}

	key, err := DeriveMachineKey()
	if err != nil {
		return fmt.Errorf("derive machine key: %w", err)
	}

	sealed, err := encrypt(key, data)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}

	encPath := filepath.Join(exeDir, encryptedEnvFileName)
	if err := os.WriteFile(encPath, []byte(encodeBlob(sealed)), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", encryptedEnvFileName, err)
	}
	if err := os.Remove(plainPath); err != nil {
		return fmt.Errorf("removed %s but could not delete %s: %w", envFileName, envFileName, err)
	}
	return nil
}

// loadEncryptedEnvFile decrypts supabase.env.enc (if present and decryptable)
// and applies its key=value lines as environment defaults — same "env vars
// win" semantics as the plaintext loader. Silent on any failure: a foreign or
// corrupted file (e.g. copied from another machine) is treated as absent.
func loadEncryptedEnvFile(exeDir string) {
	encPath := filepath.Join(exeDir, encryptedEnvFileName)
	data, err := os.ReadFile(encPath)
	if err != nil {
		return
	}
	key, err := DeriveMachineKey()
	if err != nil {
		return
	}

	sealed, err := decodeBlob(strings.TrimSpace(string(data)))
	if err != nil {
		return
	}
	plain, err := decrypt(key, sealed)
	if err != nil {
		// Wrong machine or tampered file — never fail startup over this.
		return
	}
	applyEnvLines(string(plain))
}

func loadEnvFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	applyEnvLines(string(data))
}

// applyEnvLines parses key=value lines (blank lines and # comments ignored)
// and sets them as environment variables, but never overwrites a value that
// is already set — explicit env vars always win.
func applyEnvLines(content string) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		if key != "" && os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}
