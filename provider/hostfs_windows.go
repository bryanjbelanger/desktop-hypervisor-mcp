//go:build windows

package provider

import (
	"golang.org/x/sys/windows"
)

// FreeBytes reports available bytes on the volume holding path, or -1 when
// path is empty or the volume cannot be queried.
func FreeBytes(path string) int64 {
	if path == "" {
		return -1
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return -1
	}
	var freeToCaller, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeToCaller, &total, &free); err != nil {
		return -1
	}
	return int64(freeToCaller)
}
