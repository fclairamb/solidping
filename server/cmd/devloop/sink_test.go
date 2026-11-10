package main

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

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
