package checkredis_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkredis"
)

// TestRedisPathHostRefused: a Unix socket path typed into host (issue #999)
// is refused on save with a pointer to the socket field, instead of being
// dialed as `<path>:6379` and failing with no hint.
func TestRedisPathHostRefused(t *testing.T) {
	t.Parallel()

	for _, host := range []string{
		"/run/valkey/valkey.sock",
		"/run/valkey/",
		"/run/valkey",
		"/var/run/valkey/",
		"@valkey",
	} {
		t.Run(host, func(t *testing.T) {
			t.Parallel()

			spec := &checkerdef.CheckSpec{Config: map[string]any{"host": host}}
			err := (&checkredis.RedisChecker{}).Validate(spec)
			require.ErrorContains(t, err, "socket field")
			require.ErrorContains(t, err, checkerdef.LocalSocketSetting)
		})
	}

	t.Run("hostname still valid", func(t *testing.T) {
		t.Parallel()

		spec := &checkerdef.CheckSpec{Config: map[string]any{"host": "localhost", "port": 6379}}
		require.NoError(t, (&checkredis.RedisChecker{}).Validate(spec))
	})
}
