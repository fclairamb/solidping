package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStatusLabel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("no data", statusLabel("STALE"))
	r.Equal("no data", statusLabel("stale"))
	r.Equal("UP", statusLabel("UP"))
}
