package backend_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
)

// Executor capabilities join a reported set and never a nil one, which
// would turn "unknown" into "no IPv4/IPv6".
func TestWithTaskCapabilities(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Nil(backend.WithTaskCapabilities(nil, []string{"crawl"}))
	r.Equal([]string{"ipv4", "crawl"}, backend.WithTaskCapabilities([]string{"ipv4"}, []string{"crawl"}))
	r.Equal([]string{}, backend.WithTaskCapabilities([]string{}, nil))

	base := []string{"ipv4"}
	_ = backend.WithTaskCapabilities(base, []string{"crawl"})
	r.Equal([]string{"ipv4"}, base, "the input set is not mutated")
}
