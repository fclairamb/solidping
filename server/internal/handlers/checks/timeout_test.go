package checks

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// TestValidateConfigTimeout pins the uniform per-check timeout rule (spec
// 2026-07-11-05 + explicit 1–30s range from spec 2026-07-11-09): present
// values must be duration strings in [1s, 30s]; absent keys are fine (the
// uniform 15s default applies); sub-second values are now rejected by the 1s
// floor.
func TestValidateConfigTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  map[string]any
		wantErr string // empty = valid
	}{
		{name: "absent key is valid", config: map[string]any{"url": "https://example.com"}},
		{name: "nil value is treated as absent", config: map[string]any{configKeyTimeout: nil}},
		{name: "1s floor is valid", config: map[string]any{configKeyTimeout: "1s"}},
		{name: "10s is valid", config: map[string]any{configKeyTimeout: "10s"}},
		{name: "30s boundary is valid", config: map[string]any{configKeyTimeout: "30s"}},
		{
			name:    "sub-second is rejected by the 1s floor",
			config:  map[string]any{configKeyTimeout: "500ms"},
			wantErr: "must be >= 1s and <= 30s",
		},
		{name: "31s exceeds the cap", config: map[string]any{configKeyTimeout: "31s"}, wantErr: "must be >= 1s and <= 30s"},
		{name: "1m exceeds the cap", config: map[string]any{configKeyTimeout: "1m"}, wantErr: "must be >= 1s and <= 30s"},
		{name: "zero is rejected", config: map[string]any{configKeyTimeout: "0s"}, wantErr: "must be >= 1s and <= 30s"},
		{name: "negative is rejected", config: map[string]any{configKeyTimeout: "-5s"}, wantErr: "must be >= 1s and <= 30s"},
		{
			name:    "empty string is rejected",
			config:  map[string]any{configKeyTimeout: ""},
			wantErr: "must be a valid duration string",
		},
		{
			name:    "garbage string is rejected",
			config:  map[string]any{configKeyTimeout: "abc"},
			wantErr: "must be a valid duration string",
		},
		{
			name:    "number is rejected",
			config:  map[string]any{configKeyTimeout: float64(10)},
			wantErr: "must be a duration string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			err := validateConfigTimeout(tt.config)
			if tt.wantErr == "" {
				r.NoError(err)

				return
			}

			r.Error(err)
			configErr := checkerdef.IsConfigError(err)
			r.NotNil(configErr, "timeout violations must be ConfigErrors so handlers emit VALIDATION_ERROR")
			r.Equal(configKeyTimeout, configErr.Parameter)
			r.Contains(configErr.Message, tt.wantErr)
		})
	}
}
