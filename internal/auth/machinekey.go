package auth

import (
	"crypto/sha256"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// deriveMachineKey produces a 32-byte AES key bound to this machine.
//
// Why machine-specific: a copied session/license file on another host cannot be
// decrypted without the same hardware/OS identity inputs, reducing token theft value.
func DeriveMachineKey() ([32]byte, error) {
	return deriveMachineKey()
}

func deriveMachineKey() ([32]byte, error) {
	parts, err := machineFingerprintParts()
	if err != nil {
		return [32]byte{}, err
	}
	material := strings.Join(parts, "\x00")
	// Single SHA-256 is sufficient here: inputs are high-entropy machine identifiers.
	return sha256.Sum256([]byte(material)), nil
}

func machineFingerprintParts() ([]string, error) {
	var parts []string

	host, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("hostname: %w", err)
	}
	parts = append(parts, host)

	parts = append(parts, runtime.GOOS, runtime.GOARCH)

	if id := readOSMachineID(); id != "" {
		parts = append(parts, id)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home dir: %w", err)
	}
	parts = append(parts, home)

	if len(parts) < 3 {
		return nil, fmt.Errorf("insufficient machine identity material")
	}
	return parts, nil
}
