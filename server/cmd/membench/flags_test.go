package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseFlagsDefaults(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	opts := parseFlags()
	r.Equal("local", opts.mode)
	r.Equal("idle-all-sqlite", opts.scenarios)
	r.Equal(defaultReps, opts.reps)
	r.Equal(defaultPort, opts.port)
	r.False(opts.floor)
}
