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
	// one file portable across every unix the service runs on. Saturate rather than
	// wrapping if an exotic filesystem reports a capacity beyond int64's range.
	const maxInt64 = 1<<63 - 1
	// #nosec G115 -- Bavail and Bsize are non-negative capacity counters; the
	// saturation check below covers the only overflow this can produce.
	avail, blockSize := uint64(fs.Bavail), uint64(fs.Bsize)
	if blockSize != 0 && avail > uint64(maxInt64)/blockSize {
		return maxInt64, true
	}
	// #nosec G115 -- the bound above proves avail*blockSize fits in int64.
	return int64(avail * blockSize), true
}
