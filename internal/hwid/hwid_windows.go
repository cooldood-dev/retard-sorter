//go:build windows

// File: internal/hwid/hwid_windows.go

package hwid

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

// ASSUMPTION: the app targets Windows only (matches the project setup).
// If other platforms are ever needed, hwid_linux.go and hwid_darwin.go
// implement the same platformHWID() contract.
//
// Identifiers used:
//   - BIOS/system UUID via WMI (`wmic csproduct get uuid`, PowerShell CIM
//     fallback for Win10 1903+ where wmic is deprecated). Set by the
//     motherboard firmware, stable across OS reinstalls, not editable by an
//     end user.
//   - Disk volume serial via kernel32 GetVolumeInformationW on C:\ — set at
//     format time, survives reinstalls.
//   - CPU name via registry (HARDWARE\DESCRIPTION\...\ProcessorNameString).
//
// Deliberately NOT used: MAC address (spoofable), Windows MachineGuid
// (changes on every OS reinstall), environment variables or config files
// (trivially editable).

var uuidRe = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

var (
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	getVolumeInfoProc = kernel32.NewProc("GetVolumeInformationW")
)

func platformHWID() []string {
	var parts []string
	if v := systemUUID(); v != "" {
		parts = append(parts, "bios:"+v)
	}
	if v := volumeSerial(`C:\`); v != "" {
		parts = append(parts, "disk:"+v)
	}
	if v := cpuName(); v != "" {
		parts = append(parts, "cpu:"+v)
	}
	return parts
}

func systemUUID() string {
	out, err := exec.Command("wmic", "csproduct", "get", "uuid").Output()
	if err == nil {
		if m := uuidRe.FindString(string(out)); m != "" {
			return strings.ToLower(m)
		}
	}
	out, err = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"(Get-CimInstance Win32_ComputerSystemProduct).UUID").Output()
	if err == nil {
		if m := uuidRe.FindString(string(out)); m != "" {
			return strings.ToLower(m)
		}
	}
	return ""
}

func volumeSerial(root string) string {
	var serial uint32
	rootPtr, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return ""
	}
	r1, _, _ := getVolumeInfoProc.Call(
		uintptr(unsafe.Pointer(rootPtr)), // lpRootPathName
		0, 0,                              // volume name buffer (unused)
		uintptr(unsafe.Pointer(&serial)),  // lpVolumeSerialNumber
		0, 0,                              // max component length / flags (unused)
		0, 0,                              // fs name buffer (unused)
	)
	if r1 == 0 {
		return ""
	}
	return fmt.Sprintf("%08X", serial)
}

func cpuName() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	name, _, err := k.GetStringValue("ProcessorNameString")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(name)
}
