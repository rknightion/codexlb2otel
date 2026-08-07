//go:build unix

package main

import "syscall"

// diskFree reports the bytes available to an unprivileged user on the filesystem
// holding path, and whether that could be determined.
//
// Bavail rather than Bfree: Bfree counts blocks reserved for root, which this will
// never be able to use, so reporting it would overstate the headroom on exactly the
// filesystem that is about to fill up.
func diskFree(path string) (int64, bool) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return 0, false
	}
	// Bsize is int64 on Linux and uint32 on Darwin; converting both sides keeps this
	// one file portable across every unix the service runs on.
	return int64(uint64(fs.Bavail) * uint64(fs.Bsize)), true
}
