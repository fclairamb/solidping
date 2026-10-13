//go:build !unix

package sqlite

// freeDiskBytes is unknown on this platform: the backup is attempted anyway.
func freeDiskBytes(string) (uint64, bool) { return 0, false }
