package version

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Mutates the package-level Version, so t.Parallel is intentionally omitted.
//
//nolint:paralleltest // shares package-level state with other tests in this package
func TestGetStripsLeadingV(t *testing.T) {
	t.Cleanup(func() { Version = "dev" })

	r := require.New(t)

	Version = "v1.2.3"
	r.Equal("1.2.3", Get().Version)

	Version = "1.2.3"
	r.Equal("1.2.3", Get().Version)

	Version = "dev"
	r.Equal("dev", Get().Version)
}
