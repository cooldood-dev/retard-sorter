// File: internal/hwid/hwid.go

package hwid

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
)

var (
	cacheOnce sync.Once
	cachedID  string
	cachedErr error
)

// GetHWID returns a stable identifier for this machine: SHA-256 over the
// platform hardware identifiers (motherboard/BIOS UUID, disk volume serial,
// CPU id), hex-encoded into a fixed-length string.
//
// Same value every call on the same machine, different on every other
// machine. Nothing network-related is used (MACs are spoofable / multiple).
// Cached in memory after the first computation per process run — the
// platform probe (which may shell out or read sysfs) runs exactly once.
func GetHWID() (string, error) {
	cacheOnce.Do(func() {
		parts := platformHWID()
		if len(parts) == 0 {
			cachedErr = errors.New("hwid: no stable hardware identifiers available")
			return
		}
		sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
		cachedID = hex.EncodeToString(sum[:])
	})
	return cachedID, cachedErr
}
