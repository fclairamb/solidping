package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkhttp"
)

func TestRedactURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "userinfo password is replaced",
			in:   "https://alice:s3cr3t@example.com/health?token=x",
			want: "https://alice:xxxxx@example.com/health?token=x",
		},
		{
			name: "username alone survives",
			in:   "https://alice@example.com/health",
			want: "https://alice@example.com/health",
		},
		{
			name: "plain URL is untouched",
			in:   "https://example.com/health",
			want: "https://example.com/health",
		},
		{
			name: "empty stays empty",
			in:   "",
			want: "",
		},
		{
			name: "unparsable input is passed through unchanged",
			in:   "http://a b.com/%zz",
			want: "http://a b.com/%zz",
		},
		{
			name: "garbage that is not a URL at all",
			in:   "not a url ://::",
			want: "not a url ://::",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, checkhttp.RedactURL(tc.in))
		})
	}
}

// The credential must not reach the log through the wrapped error either. The
// only error stringConfigValue produces for the password key is a fixed
// "must be a string", with no value in it.
func TestPasswordConfigErrorCarriesNoCredential(t *testing.T) {
	t.Parallel()

	cfg := &checkhttp.HTTPConfig{}

	_, err := cfg.NormalizeConfig(map[string]any{
		"username": "alice",
		"password": 12345, // wrong type: the only way this path errors
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "must be a string")
	require.NotContains(t, err.Error(), "12345")
}
