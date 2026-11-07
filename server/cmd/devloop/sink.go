package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/fclairamb/solidping/server/internal/rotatelog"
)

// multiSink fans writes out to several writers, serialized by a mutex so
// concurrent producers (devloop's own logger and the child pipe pumps) do not
// tear each other's lines within a stream. It never returns an error: a broken
// sink must not abort the writer feeding it.
type multiSink struct {
	mu      sync.Mutex
	writers []io.Writer
}

func (m *multiSink) Write(data []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, writer := range m.writers {
		_, _ = writer.Write(data)
	}
	return len(data), nil
}

// newSink builds the output destination for one supervised stream: always the
// terminal, plus a size-rotated <name>.log when a log directory is configured.
func newSink(logDir, name string, maxSize int64, backups int) io.Writer {
	if logDir == "" {
		return os.Stdout
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "[devloop] cannot create log dir %s: %v (file logging disabled)\n", logDir, err)
		return os.Stdout
	}
	rotating := rotatelog.New(filepath.Join(logDir, name+".log"), maxSize, backups)
	return &multiSink{writers: []io.Writer{os.Stdout, rotating}}
}
