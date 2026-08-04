//go:build darwin

// File: internal/hwid/hwid_darwin.go

package hwid

import (
	"os/exec"
	"strings"
)

// IOPlatformUUID is the hardware UUID stored in Apple firmware (the same
// identifier Apple uses for device registration). It is unique per device
// and survives OS reinstalls.
func platformHWID() []string {
	out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, `"IOPlatformUUID"`) {
			if i := strings.Index(line, "="); i >= 0 {
				v := strings.Trim(strings.TrimSpace(line[i+1:]), `"`)
				if v != "" {
					return []string{"bios:" + v}
				}
			}
		}
	}
	return nil
}
