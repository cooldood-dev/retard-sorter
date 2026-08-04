// File: internal/auth/session.go
//
// Encrypted local session storage. The session (Supabase access/refresh
// tokens plus the activated license key) is written to disk AES-256-GCM
// encrypted; tokens are never stored in plaintext.

package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"retard-sorter/internal/hwid"
)

// ErrNoSession is returned whenever there is no usable session: missing
// file, corrupt ciphertext, or no license key recorded. Callers must treat
// it as "not activated".
var ErrNoSession = errors.New("no valid session")

// Session is the persisted, encrypted activation state.
//
// There is no account login: activation is license-key only. The session
// records the activated key so the background revalidator can re-check it
// without asking the user. License validity (not a token TTL) is what keeps
// the session alive — the revalidator clears it on a definitive
// invalidation verdict.
type Session struct {
	LicenseKey string    `json:"license_key"`
	ActivatedAt time.Time `json:"activated_at"`
}

// sessionPath returns the encrypted session file location. Overridable via
// the SESSION_FILE env var (used by tests); defaults to the per-user config
// directory.
func sessionPath() (string, error) {
	if p := os.Getenv("SESSION_FILE"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	appDir := filepath.Join(dir, "retard-sorter")
	if err := os.MkdirAll(appDir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(appDir, "session.enc"), nil
}

// sessionKey derives the AES-256 key from GetHWID(). Chosen over a random
// key stored in a file because it binds the session to this machine: copied
// to another PC, decryption fails — and there is no key file to copy
// alongside it.
func sessionKey() ([]byte, error) {
	id, err := hwid.GetHWID()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(id))
	return sum[:], nil
}

// SaveSession encrypts the session with AES-256-GCM and writes it to disk
// atomically (tmp file + rename). Tokens are never written in plaintext.
func SaveSession(s Session) error {
	if s.LicenseKey == "" {
		return errors.New("refusing to save a session without a license key")
	}
	path, err := sessionPath()
	if err != nil {
		return err
	}
	key, err := sessionKey()
	if err != nil {
		return err
	}
	plain, err := json.Marshal(s)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	sealed := gcm.Seal(nonce, nonce, plain, nil)

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, sealed, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadSession decrypts and returns the session. Missing file, corrupt
// ciphertext (including a file copied from another machine — wrong key),
// or a session without a license key all return ErrNoSession.
func LoadSession() (*Session, error) {
	path, err := sessionPath()
	if err != nil {
		return nil, ErrNoSession
	}
	sealed, err := os.ReadFile(path)
	if err != nil {
		// Missing or unreadable — fail closed, no session.
		return nil, ErrNoSession
	}
	key, err := sessionKey()
	if err != nil {
		return nil, ErrNoSession
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrNoSession
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrNoSession
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, ErrNoSession
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
	if err != nil {
		// Corrupt or wrong machine.
		return nil, ErrNoSession
	}
	var s Session
	if err := json.Unmarshal(plain, &s); err != nil {
		return nil, ErrNoSession
	}
	if s.LicenseKey == "" {
		return nil, ErrNoSession
	}
	return &s, nil
}

// ClearSession deletes the local session file. Used by revalidation when
// the license is definitively invalid.
func ClearSession() error {
	path, err := sessionPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
