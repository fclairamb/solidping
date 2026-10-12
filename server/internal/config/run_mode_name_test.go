package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRunModeName pins the display name logged at boot: an unset run mode is
// the production mode, never an empty string.
func TestRunModeName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		runMode string
		want    string
	}{
		{runMode: "", want: RunModeProduction},
		{runMode: "test", want: "test"},
		{runMode: "demo", want: "demo"},
	}

	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()

			cfg := &Config{RunMode: tc.runMode}
			require.Equal(t, tc.want, cfg.RunModeName())
		})
	}
}
