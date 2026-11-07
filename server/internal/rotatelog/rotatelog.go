// Package rotatelog provides a size-rotated log file writer.
package rotatelog

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"sync"
)

// Defaults for New's maxSize and backups.
const (
	DefaultMaxSize = 20 << 20 // 20 MB per active log file.
	DefaultBackups = 2
)

// Writer appends to a log file and rotates it at line boundaries once
// the active file reaches maxSize: path.1 shifts to path.2 (and so on up to
// backups), path shifts to path.1, and anything older is dropped. On creation
// a non-empty pre-existing file is rotated away so the active file always
// starts fresh for each run while the previous run's tail survives as
// path.1. The writer never returns an error: on open or write failure it warns
// once on stderr and degrades to a no-op, because file logging must never take
// down the process it logs for.
//
// The active file is preferentially cut at a line boundary, so it may overshoot
// maxSize by the remainder of the current line — but never past a hard ceiling
// of 2*maxSize, at which point it is force-cut mid-line. That ceiling is what
// keeps a runaway producer (e.g. a backend hot-looping on an error with no newline,
// or emitting one unbounded line) from growing the log without limit: total
// on-disk size stays bounded by 2*maxSize*(backups+1) whatever the producer does.
type Writer struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	backups int
	file    *os.File
	size    int64
}

// New opens path for writing, rotating away a non-empty previous file first.
// A maxSize of zero or less means DefaultMaxSize.
func New(path string, maxSize int64, backups int) *Writer {
	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}
	if backups < 0 {
		backups = 0
	}
	writer := &Writer{path: path, maxSize: maxSize, backups: backups}
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		writer.shiftBackups() // Keep the previous run's tail around as path.1.
	}
	writer.open()
	return writer
}

// Write implements io.Writer. It always reports full success so a failing file
// never breaks the logger (or multi-writer) feeding it.
func (w *Writer) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	total := len(data)
	for len(data) > 0 && w.file != nil {
		cut := w.rotationCut(data)
		if cut < 0 {
			w.writeFile(data)
			break
		}
		w.writeFile(data[:cut])
		w.rotate()
		data = data[cut:]
	}
	return total, nil
}

// rotationCut returns the index at which the active file must rotate, or -1 when
// the whole chunk can be appended to the current file. Rotation is preferred at
// the first line boundary at or after maxSize so a line is not split across
// files. A stream that emits no newline within reach is tolerated only until the
// active file would reach the hard ceiling (2*maxSize), past which it is force-
// cut mid-line — otherwise a newline-less or pathologically long line would grow
// the file without bound.
func (w *Writer) rotationCut(data []byte) int {
	remaining := w.maxSize - w.size
	if int64(len(data)) < remaining {
		return -1
	}
	searchFrom := remaining - 1
	if searchFrom < 0 {
		searchFrom = 0
	}
	if idx := bytes.IndexByte(data[searchFrom:], '\n'); idx >= 0 {
		return int(searchFrom) + idx + 1
	}
	// No line boundary within reach: keep the line intact until the hard ceiling,
	// then force a mid-line cut so a newline-less stream can't grow unbounded.
	hardRemaining := 2*w.maxSize - w.size
	if int64(len(data)) < hardRemaining {
		return -1
	}
	if hardRemaining < 1 {
		hardRemaining = 1 // always make progress toward a rotation
	}
	return int(hardRemaining)
}

func (w *Writer) writeFile(data []byte) {
	written, err := w.file.Write(data)
	w.size += int64(written)
	if err != nil {
		w.disable(err)
	}
}

// disable turns file logging off after warning once on stderr.
func (w *Writer) disable(err error) {
	_, _ = fmt.Fprintf(os.Stderr, "[rotatelog] file logging to %s disabled: %v\n", w.path, err)
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
}

func (w *Writer) rotate() {
	if w.file == nil {
		return
	}
	_ = w.file.Close()
	w.file = nil
	w.shiftBackups()
	w.open()
}

// shiftBackups slides path.1 to path.2 (and so on) then path to path.1,
// dropping the oldest backup beyond the configured count.
func (w *Writer) shiftBackups() {
	if w.backups < 1 {
		_ = os.Remove(w.path)
		return
	}
	_ = os.Remove(w.backupPath(w.backups))
	for i := w.backups - 1; i >= 1; i-- {
		_ = os.Rename(w.backupPath(i), w.backupPath(i+1))
	}
	_ = os.Rename(w.path, w.backupPath(1))
}

func (w *Writer) backupPath(index int) string {
	return w.path + "." + strconv.Itoa(index)
}

// open truncates and (re)creates the active file; on failure the writer
// degrades to a no-op.
func (w *Writer) open() {
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		w.disable(err)
		return
	}
	w.file = file
	w.size = 0
}
