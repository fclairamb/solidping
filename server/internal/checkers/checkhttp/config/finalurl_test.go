package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestNormalizeFinalURL(t *testing.T) {
	t.Parallel()

	tests := []struct{ in, want string }{
		{"HTTPS://Acme.COM", "https://acme.com/"},
		{"https://acme.com:443/x", "https://acme.com/x"},
		{"http://acme.com:80", "http://acme.com/"},
		{"http://acme.com:8080/", "http://acme.com:8080/"},
		{"https://acme.com:80/", "https://acme.com:80/"},
		{"https://acme.com/x#frag", "https://acme.com/x"},
		{"https://acme.com/Login?a=1&b=2", "https://acme.com/Login?a=1&b=2"},
	}
	for _, tc := range tests {
		got, err := NormalizeFinalURL(tc.in)
		require.NoError(t, err, tc.in)
		require.Equal(t, tc.want, got, tc.in)
	}
}

func TestNormalizeFinalURL_Differences(t *testing.T) {
	t.Parallel()

	pairs := [][2]string{
		{"https://acme.com/Login", "https://acme.com/login"},
		{"https://acme.com/x", "https://acme.com/x/"},
		{"https://acme.com/?a=1&b=2", "https://acme.com/?b=2&a=1"},
	}
	for _, p := range pairs {
		a, err := NormalizeFinalURL(p[0])
		require.NoError(t, err)
		b, err := NormalizeFinalURL(p[1])
		require.NoError(t, err)
		require.NotEqual(t, a, b, p)
	}
}

func TestNormalizeFinalURL_Rejected(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"", "/login", "acme.com/x", "ftp://acme.com", "https://", "http://%zz"} {
		_, err := NormalizeFinalURL(in)
		require.Error(t, err, in)
	}
}

func TestExpectedFinalURL_FromMapGetConfig(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"expectedFinalUrl", "expected_final_url"} {
		r := require.New(t)
		cfg := &HTTPConfig{}
		r.NoError(cfg.FromMap(map[string]any{"url": "http://acme.com", key: "https://www.acme.com/"}))
		r.Equal("https://www.acme.com/", cfg.ExpectedFinalURL)
		r.Equal("https://www.acme.com/", cfg.GetConfig()["expectedFinalUrl"])
	}

	r := require.New(t)
	r.Error((&HTTPConfig{}).FromMap(map[string]any{"url": "http://acme.com", "expectedFinalUrl": 3}))
	r.NotContains((&HTTPConfig{URL: "http://acme.com"}).GetConfig(), "expectedFinalUrl")
}

func TestValidateSpec_ExpectedFinalURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  map[string]any
		wantErr bool
	}{
		{"ok", map[string]any{"expectedFinalUrl": "https://www.acme.com/"}, false},
		{"redirects off", map[string]any{"followRedirects": false, "expectedFinalUrl": "https://acme.com/"}, true},
		{"same-host other host", map[string]any{
			"redirectHostPolicy": "same-host", "expectedFinalUrl": "https://www.acme.com/",
		}, true},
		{"same-host same host", map[string]any{
			"redirectHostPolicy": "same-host", "expectedFinalUrl": "HTTPS://ACME.com/login",
		}, false},
		{"invalid", map[string]any{"expectedFinalUrl": "/login"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			tc.config["url"] = "https://acme.com"

			err := ValidateSpec(&checkerdef.CheckSpec{Config: tc.config})
			if !tc.wantErr {
				r.NoError(err)

				return
			}

			var cfgErr *checkerdef.ConfigError
			r.ErrorAs(err, &cfgErr)
			r.Equal("expectedFinalUrl", cfgErr.Parameter)
		})
	}
}
