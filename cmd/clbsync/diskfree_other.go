//go:build !unix

package main

// diskFree is unavailable here; the report simply omits free space rather than
// guessing at it.
func diskFree(string) (int64, bool) { return 0, false }
