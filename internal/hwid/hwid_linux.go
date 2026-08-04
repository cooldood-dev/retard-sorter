//go:build linux

// File: internal/hwid/hwid_linux.go

package hwid

import (
	"os"
	"strings"
)

// Identifiers:
//   - DMI product UUID (/sys/class/dmi/id/product_uuid) — set by the
//     motherboard firmware, stable across reboots AND OS reinstalls.
//   - /etc/machine-id — stable across reboots; changes on a fresh install,
//     kept as a secondary component only (the UUID is the primary).
//
// Disk volume serial on Linux is per-device and awkward to map to "the
// machine", so it is intentionally omitted here.
func platformHWID() []string {
	var parts []string
	if b, err := os.ReadFile("/sys/class/dmi/id/product_uuid"); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			parts = append(parts, "bios:"+v)
		}
	}
	if b, err := os.ReadFile("/etc/machine-id"); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			parts = append(parts, "machineid:"+v)
		}
	}
	return parts
}
