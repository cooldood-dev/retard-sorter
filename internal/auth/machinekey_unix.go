//go:build !windows

package auth

import (
	"os"
	"strings"
)

func readOSMachineID() string {
	for _, path := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id
		}
	}
	return ""
}
