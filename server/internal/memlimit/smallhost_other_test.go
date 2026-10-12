//go:build !linux

package memlimit

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Off Linux there is no cgroup or MemTotal to read, so no host is "small".
func TestSmallHostNonLinux(t *testing.T) {
	t.Parallel()

	require.False(t, SmallHost())
	_, ok := EffectiveMemoryBytes()
	require.False(t, ok)
}
