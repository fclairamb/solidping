//go:build cgo

package buildinfo

// cgoEnabled is true in cgo-enabled builds. With cgo on, sqliteshim selects the
// mattn driver, whose memory is allocated off the Go heap.
const cgoEnabled = true
