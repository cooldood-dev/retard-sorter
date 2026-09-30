package converter

import (
	"crypto/rand"
	"encoding/base32"
	"os"
	"path/filepath"
	"strings"
)

var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// randomSuffix returns a cryptographically random 8-12 char alphanumeric
// string (base32, lowercase, no padding).
func randomSuffix() string {
	n := 8 + (int(randByte()) % 5) // 8..12
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		// Unrecoverable in practice; fall back to a fixed-width derivation.
		raw = []byte("c0nv3rt3r")
	}
	s := strings.ToLower(base32NoPad.EncodeToString(raw))
	if len(s) > n {
		s = s[:n]
	}
	return s
}

func randByte() byte {
	b := make([]byte, 1)
	rand.Read(b)
	return b[0]
}

// NewCookieFileName builds a unique "retard_<random>.txt" name inside dir,
// retrying the suffix if the file somehow already exists.
func NewCookieFileName(dir, prefix string) (string, error) {
	for i := 0; i < 16; i++ {
		name := prefix + "_" + randomSuffix() + ".txt"
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return name, nil
		}
	}
	return "", os.ErrExist
}

// WriteAtomic writes data to a temp file in dir and renames it to name,
// guaranteeing the destination never holds a partial file.
func WriteAtomic(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".cvt-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(dir, name))
}
