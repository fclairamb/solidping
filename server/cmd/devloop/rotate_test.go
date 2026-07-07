package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// testLine returns a numbered 30-byte line (29 chars + newline) so line
// integrity across rotated files can be asserted exactly.
func testLine(index int) string {
	return fmt.Sprintf("line-%04d-%s\n", index, strings.Repeat("x", 19))
}

// requireIntactLines asserts a file contains only complete test lines and
// returns them.
func requireIntactLines(t *testing.T, path string) []string {
	t.Helper()
	r := require.New(t)
	content, err := os.ReadFile(path)
	r.NoError(err)
	if len(content) == 0 {
		return nil
	}
	r.True(strings.HasSuffix(string(content), "\n"), "file %s must end on a line boundary", path)
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	for _, line := range lines {
		r.Len(line+"\n", 30, "torn line %q in %s", line, path)
		r.True(strings.HasPrefix(line, "line-"), "torn line %q in %s", line, path)
	}
	return lines
}

func TestRotatingWriterRotatesShiftsAndPrunes(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	path := filepath.Join(t.TempDir(), "backend.log")

	// 30-byte lines against a 100-byte cap: rotation lands after every 4th
	// line (120 bytes, one line of slack past the cap).
	writer := newRotatingWriter(path, 100, 2)
	for i := range 41 {
		n, err := writer.Write([]byte(testLine(i)))
		r.NoError(err)
		r.Equal(30, n)
	}

	// 41 lines = 10 full rotations of 4 lines + 1 line in the active file.
	active := requireIntactLines(t, path)
	r.Equal([]string{strings.TrimSuffix(testLine(40), "\n")}, active)

	info, err := os.Stat(path)
	r.NoError(err)
	r.LessOrEqual(info.Size(), int64(100+30), "active file exceeds cap plus one line of slack")

	backup1 := requireIntactLines(t, path+".1")
	r.Len(backup1, 4)
	r.Equal(strings.TrimSuffix(testLine(36), "\n"), backup1[0])

	backup2 := requireIntactLines(t, path+".2")
	r.Len(backup2, 4)
	r.Equal(strings.TrimSuffix(testLine(32), "\n"), backup2[0])

	_, err = os.Stat(path + ".3")
	r.True(os.IsNotExist(err), "backups beyond the configured count must be pruned")
}

func TestRotatingWriterSplitsLargeWriteAtLineBoundaries(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	path := filepath.Join(t.TempDir(), "backend.log")

	var input strings.Builder
	for i := range 10 {
		input.WriteString(testLine(i))
	}

	writer := newRotatingWriter(path, 100, 2)
	n, err := writer.Write([]byte(input.String()))
	r.NoError(err)
	r.Equal(300, n)

	// One 300-byte write rotates twice at 120-byte line boundaries.
	active := requireIntactLines(t, path)
	r.Len(active, 2)
	r.Equal(strings.TrimSuffix(testLine(8), "\n"), active[0])

	backup1 := requireIntactLines(t, path+".1")
	r.Len(backup1, 4)
	r.Equal(strings.TrimSuffix(testLine(4), "\n"), backup1[0])

	backup2 := requireIntactLines(t, path+".2")
	r.Len(backup2, 4)
	r.Equal(strings.TrimSuffix(testLine(0), "\n"), backup2[0])
}

func TestRotatingWriterStartsFreshAndKeepsPreviousTail(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	path := filepath.Join(t.TempDir(), "backend.log")

	r.NoError(os.WriteFile(path, []byte("previous session tail\n"), 0o644))
	r.NoError(os.WriteFile(path+".1", []byte("older session\n"), 0o644))

	writer := newRotatingWriter(path, 200, 3)

	content, err := os.ReadFile(path)
	r.NoError(err)
	r.Empty(content, "active file must start fresh")

	shifted, err := os.ReadFile(path + ".1")
	r.NoError(err)
	r.Equal("previous session tail\n", string(shifted))

	older, err := os.ReadFile(path + ".2")
	r.NoError(err)
	r.Equal("older session\n", string(older))

	_, err = writer.Write([]byte("new session\n"))
	r.NoError(err)
	content, err = os.ReadFile(path)
	r.NoError(err)
	r.Equal("new session\n", string(content))
}

func TestRotatingWriterEmptyExistingFileIsNotRotated(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	path := filepath.Join(t.TempDir(), "backend.log")

	r.NoError(os.WriteFile(path, nil, 0o644))
	newRotatingWriter(path, 100, 2)

	_, err := os.Stat(path + ".1")
	r.True(os.IsNotExist(err), "an empty previous file must not produce a backup")
}

func TestRotatingWriterOpenFailureDegradesToNoOp(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// A directory path cannot be opened as a file.
	writer := newRotatingWriter(t.TempDir(), 50, 1)
	n, err := writer.Write([]byte("hello\n"))
	r.NoError(err, "a degraded writer must never error the stream")
	r.Equal(6, n)
}

func TestRotatingWriterWriteFailureDegradesToNoOp(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	path := filepath.Join(t.TempDir(), "backend.log")

	writer := newRotatingWriter(path, 64, 0)
	r.NotNil(writer.file)
	r.NoError(writer.file.Close()) // Break the file behind the writer's back.

	n, err := writer.Write([]byte("hello\n"))
	r.NoError(err)
	r.Equal(6, n)
	r.Nil(writer.file, "writer must disable file logging after a write failure")

	n, err = writer.Write([]byte("again\n"))
	r.NoError(err)
	r.Equal(6, n)
}

type failingWriter struct{}

var errSinkBoom = errors.New("sink boom")

func (failingWriter) Write([]byte) (int, error) { return 0, errSinkBoom }

func TestMultiSinkNeverFailsAndFansOut(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var buf strings.Builder
	sink := &multiSink{writers: []io.Writer{failingWriter{}, &buf}}
	n, err := sink.Write([]byte("hello\n"))
	r.NoError(err, "a broken sink must not abort the stream")
	r.Equal(6, n)
	r.Equal("hello\n", buf.String())
}
