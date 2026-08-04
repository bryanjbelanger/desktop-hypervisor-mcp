//go:build !windows

package provider

import "syscall"

// FreeBytes reports available bytes on the filesystem holding path, or -1
// when path is empty or cannot be stat'd.
func FreeBytes(path string) int64 {
	if path == "" {
		return -1
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}
