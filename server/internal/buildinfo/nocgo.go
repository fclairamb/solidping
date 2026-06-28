//go:build !cgo

package buildinfo

// cgoEnabled is false in non-cgo builds. With cgo off, sqliteshim selects the
// pure-Go modernc driver, whose memory is on the Go heap (visible to pprof).
const cgoEnabled = false
