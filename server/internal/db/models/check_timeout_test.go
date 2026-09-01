package models_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Check.TimeoutOrDefault is read by both consumers of the confirmation-margin
// formula (the runtime hold cap and the dependency lint), so a wrong fallback
// silently mis-sizes both. Anything that is not a positive, parseable duration
// must fall back to the server default rather than to zero.
func TestCheckTimeoutOrDefault(t *testing.T) {
	t.Parallel()

	const fallback = 15 * time.Second

	cases := []struct {
		name   string
		config models.JSONMap
		want   time.Duration
	}{
		{name: "nil config", config: nil, want: fallback},
		{name: "empty config", config: models.JSONMap{}, want: fallback},
		{name: "explicit timeout", config: models.JSONMap{"timeout": "30s"}, want: 30 * time.Second},
		{name: "sub-second timeout", config: models.JSONMap{"timeout": "250ms"}, want: 250 * time.Millisecond},
		{name: "empty string", config: models.JSONMap{"timeout": ""}, want: fallback},
		{name: "unparseable", config: models.JSONMap{"timeout": "soon"}, want: fallback},
		{name: "zero", config: models.JSONMap{"timeout": "0s"}, want: fallback},
		{name: "negative", config: models.JSONMap{"timeout": "-5s"}, want: fallback},
		{name: "wrong type", config: models.JSONMap{"timeout": 30}, want: fallback},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			check := &models.Check{Config: tc.config}
			require.Equal(t, tc.want, check.TimeoutOrDefault(fallback))
		})
	}
}
