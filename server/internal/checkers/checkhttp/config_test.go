package checkhttp

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestFromMap_ExpectedStatusCamelCase(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &HTTPConfig{}
	r.NoError(cfg.FromMap(map[string]any{
		"url":            "http://example.com",
		"expectedStatus": 401,
	}))
	r.Equal(401, cfg.ExpectedStatus)
}

func TestFromMap_ExpectedStatusSnakeCaseLegacy(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &HTTPConfig{}
	r.NoError(cfg.FromMap(map[string]any{
		"url":             "http://example.com",
		"expected_status": 401,
	}))
	r.Equal(401, cfg.ExpectedStatus)
}

func TestFromMap_ExpectedStatusCodesCamelCase(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &HTTPConfig{}
	r.NoError(cfg.FromMap(map[string]any{
		"url":                 "http://example.com",
		"expectedStatusCodes": []any{"401", "4XX"},
	}))
	r.Equal([]string{"401", "4XX"}, cfg.ExpectedStatusCodes)
}

func TestFromMap_BodyKeysCamelCase(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &HTTPConfig{}
	r.NoError(cfg.FromMap(map[string]any{
		"url":               "http://example.com",
		"bodyExpect":        "ok",
		"bodyReject":        "error",
		"bodyPattern":       "^OK",
		"bodyPatternReject": "FAIL",
		"headersPattern":    map[string]any{"X-OK": "yes"},
	}))
	r.Equal("ok", cfg.BodyExpect)
	r.Equal("error", cfg.BodyReject)
	r.Equal("^OK", cfg.BodyPattern)
	r.Equal("FAIL", cfg.BodyPatternReject)
	r.Equal(map[string]string{"X-OK": "yes"}, cfg.HeadersPattern)
}

func TestGetConfig_WritesCamelCase(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &HTTPConfig{
		URL:                 "http://example.com",
		ExpectedStatus:      401,
		ExpectedStatusCodes: []string{"4XX"},
		BodyExpect:          "ok",
	}

	out := cfg.GetConfig()
	r.Equal(401, out["expectedStatus"])
	r.Equal([]string{"4XX"}, out["expectedStatusCodes"])
	r.Equal("ok", out["bodyExpect"])
	r.NotContains(out, "expected_status")
	r.NotContains(out, "expected_status_codes")
}

func TestHTTPConfigVerifySslFollowRedirects_FromMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		input               map[string]any
		wantVerifySsl       *bool
		wantFollowRedirects *bool
		wantErr             bool
	}{
		{
			name:  "absent leaves both nil (default true)",
			input: map[string]any{"url": "http://example.com"},
		},
		{
			name: "camelCase false",
			input: map[string]any{
				"url":             "http://example.com",
				"verifySsl":       false,
				"followRedirects": false,
			},
			wantVerifySsl:       boolPtr(false),
			wantFollowRedirects: boolPtr(false),
		},
		{
			name: "camelCase true is still recorded explicitly",
			input: map[string]any{
				"url":             "http://example.com",
				"verifySsl":       true,
				"followRedirects": true,
			},
			wantVerifySsl:       boolPtr(true),
			wantFollowRedirects: boolPtr(true),
		},
		{
			name: "snake_case read fallback",
			input: map[string]any{
				"url":              "http://example.com",
				"verify_ssl":       false,
				"follow_redirects": false,
			},
			wantVerifySsl:       boolPtr(false),
			wantFollowRedirects: boolPtr(false),
		},
		{
			name: "camelCase wins over snake_case when both present",
			input: map[string]any{
				"url":              "http://example.com",
				"verifySsl":        true,
				"verify_ssl":       false,
				"followRedirects":  true,
				"follow_redirects": false,
			},
			wantVerifySsl:       boolPtr(true),
			wantFollowRedirects: boolPtr(true),
		},
		{
			name: "non-boolean verifySsl is a config error",
			input: map[string]any{
				"url":       "http://example.com",
				"verifySsl": "no",
			},
			wantErr: true,
		},
		{
			name: "non-boolean followRedirects is a config error",
			input: map[string]any{
				"url":             "http://example.com",
				"followRedirects": "no",
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			cfg := &HTTPConfig{}
			err := cfg.FromMap(tc.input)

			if tc.wantErr {
				r.Error(err)

				return
			}

			r.NoError(err)
			r.Equal(tc.wantVerifySsl, cfg.VerifySsl)
			r.Equal(tc.wantFollowRedirects, cfg.FollowRedirects)
		})
	}
}

func TestHTTPConfigVerifySslFollowRedirects_GetConfig(t *testing.T) {
	t.Parallel()

	t.Run("unset omits both keys", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		cfg := &HTTPConfig{URL: "http://example.com"}
		out := cfg.GetConfig()
		r.NotContains(out, "verifySsl")
		r.NotContains(out, "followRedirects")
	})

	t.Run("explicit true omits both keys (matches the default)", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		verify, follow := true, true
		cfg := &HTTPConfig{URL: "http://example.com", VerifySsl: &verify, FollowRedirects: &follow}
		out := cfg.GetConfig()
		r.NotContains(out, "verifySsl")
		r.NotContains(out, "followRedirects")
	})

	t.Run("explicit false emits both keys", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		verify, follow := false, false
		cfg := &HTTPConfig{URL: "http://example.com", VerifySsl: &verify, FollowRedirects: &follow}
		out := cfg.GetConfig()
		r.Equal(false, out["verifySsl"])
		r.Equal(false, out["followRedirects"])
	})
}

func TestHTTPConfigVerifySslFollowRedirects_RoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	verify, follow := false, false
	original := &HTTPConfig{URL: "http://example.com", VerifySsl: &verify, FollowRedirects: &follow}

	roundTripped := &HTTPConfig{}
	r.NoError(roundTripped.FromMap(original.GetConfig()))

	r.True(roundTripped.SkipTLSVerify())
	r.True(roundTripped.SkipRedirects())
}

func TestHTTPConfigSecretHeaders_RoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &HTTPConfig{
		URL:           "http://example.com",
		SecretHeaders: map[string]string{"x-api-key": "secret", "Authorization": "Bearer tok"},
	}
	m := cfg.GetConfig()

	// SecretHeaders should be present in GetConfig output
	r.Contains(m, "secretHeaders")

	cfg2 := &HTTPConfig{}
	r.NoError(cfg2.FromMap(m))
	r.Equal(cfg.SecretHeaders, cfg2.SecretHeaders)
}

func TestHTTPConfigSecretHeaders_FromMapAny(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &HTTPConfig{}
	r.NoError(cfg.FromMap(map[string]any{
		"url": "http://example.com",
		"secretHeaders": map[string]any{
			"x-api-key": "sk-test",
		},
	}))
	r.Equal(map[string]string{"x-api-key": "sk-test"}, cfg.SecretHeaders)
}

func TestHTTPConfigSecretHeaders_EmptyNotIncluded(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &HTTPConfig{URL: "http://example.com"}
	m := cfg.GetConfig()
	r.NotContains(m, "secretHeaders")
}

func TestHTTPConfigSecretHeaders_InvalidType(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &HTTPConfig{}
	err := cfg.FromMap(map[string]any{
		"url":           "http://example.com",
		"secretHeaders": "not-a-map",
	})
	r.Error(err)
}

func TestHTTPConfigSecretFields(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &HTTPConfig{}
	fields := cfg.SecretFields()
	// `basicAuth` is the canonical home; `password` stays listed so legacy
	// rows keep their encrypted half and survive a PATCH. `username` must NOT
	// be listed — it would be stripped from exports and swept into the
	// encrypted blob by credmigrate.
	r.Equal([]string{"basicAuth", "password", "secretHeaders"}, fields)
	r.NotContains(fields, "username")
}

func TestHTTPConfigBasicAuth_RoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &HTTPConfig{URL: "http://example.com", BasicAuth: "alice:hunter2"}
	m := cfg.GetConfig()
	r.Equal("alice:hunter2", m["basicAuth"])

	cfg2 := &HTTPConfig{}
	r.NoError(cfg2.FromMap(m))
	r.Equal("alice:hunter2", cfg2.BasicAuth)
}

func TestHTTPConfigBasicAuth_EmptyNotIncluded(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	m := (&HTTPConfig{URL: "http://example.com"}).GetConfig()
	r.NotContains(m, "basicAuth")
}

func TestHTTPConfigBasicAuth_InvalidType(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	err := (&HTTPConfig{}).FromMap(map[string]any{"url": "http://example.com", "basicAuth": 42})
	r.Error(err)
}

func TestHTTPConfigBasicAuthCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      HTTPConfig
		username string
		password string
		ok       bool
	}{
		{name: "none", cfg: HTTPConfig{}, ok: false},
		{
			name: "folded", cfg: HTTPConfig{BasicAuth: "alice:hunter2"},
			username: "alice", password: "hunter2", ok: true,
		},
		{
			name: "folded password containing a colon",
			cfg:  HTTPConfig{BasicAuth: "alice:hun:ter2"}, username: "alice", password: "hun:ter2", ok: true,
		},
		{name: "folded without a password", cfg: HTTPConfig{BasicAuth: "alice:"}, username: "alice", ok: true},
		{
			name: "legacy pair", cfg: HTTPConfig{Username: "bob", Password: "pw"},
			username: "bob", password: "pw", ok: true,
		},
		{
			name:     "folded wins over the legacy pair",
			cfg:      HTTPConfig{BasicAuth: "alice:hunter2", Username: "bob", Password: "pw"},
			username: "alice", password: "hunter2", ok: true,
		},
		{name: "legacy password only sends nothing", cfg: HTTPConfig{Password: "pw"}, ok: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			username, password, ok := tc.cfg.BasicAuthCredentials()
			r.Equal(tc.ok, ok)
			r.Equal(tc.username, username)
			r.Equal(tc.password, password)
		})
	}
}

func TestHTTPConfigNormalizeConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   map[string]any
		want map[string]any
	}{
		{
			name: "folds the pair",
			in:   map[string]any{"url": "u", "username": "alice", "password": "hunter2"},
			want: map[string]any{"url": "u", "basicAuth": "alice:hunter2"},
		},
		{
			name: "folds a username with no password",
			in:   map[string]any{"url": "u", "username": "alice"},
			want: map[string]any{"url": "u", "basicAuth": "alice:"},
		},
		{
			name: "overwrites a preserved value",
			in:   map[string]any{"url": "u", "basicAuth": "old:old", "username": "alice", "password": "hunter2"},
			want: map[string]any{"url": "u", "basicAuth": "alice:hunter2"},
		},
		{
			name: "drops a stray password with no username",
			in:   map[string]any{"url": "u", "password": "hunter2"},
			want: map[string]any{"url": "u"},
		},
		{
			name: "drops a stray password and keeps a preserved credential",
			in:   map[string]any{"url": "u", "basicAuth": "alice:hunter2", "password": "stray"},
			want: map[string]any{"url": "u", "basicAuth": "alice:hunter2"},
		},
		{
			name: "an empty username does not fold",
			in:   map[string]any{"url": "u", "username": "", "password": "hunter2"},
			want: map[string]any{"url": "u"},
		},
		{
			name: "no-op without any auth keys",
			in:   map[string]any{"url": "u", "method": "POST"},
			want: map[string]any{"url": "u", "method": "POST"},
		},
		{
			name: "preserves a folded credential on an unrelated config",
			in:   map[string]any{"url": "u", "basicAuth": "alice:hunter2"},
			want: map[string]any{"url": "u", "basicAuth": "alice:hunter2"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			got, err := (&HTTPConfig{}).NormalizeConfig(tc.in)
			r.NoError(err)
			r.Equal(tc.want, got)

			// Idempotent: re-normalizing must not change anything (the same
			// manifest can be applied repeatedly).
			again, err := (&HTTPConfig{}).NormalizeConfig(got)
			r.NoError(err)
			r.Equal(tc.want, again)
		})
	}
}

func TestHTTPConfigNormalizeConfig_DoesNotMutateInput(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	in := map[string]any{"url": "u", "username": "alice", "password": "hunter2"}
	_, err := (&HTTPConfig{}).NormalizeConfig(in)
	r.NoError(err)
	r.Equal(map[string]any{"url": "u", "username": "alice", "password": "hunter2"}, in)
}

func TestHTTPConfigNormalizeConfig_RejectsColonInUsername(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, err := (&HTTPConfig{}).NormalizeConfig(map[string]any{"url": "u", "username": "al:ice", "password": "x"})
	r.Error(err)

	configErr := checkerdef.IsConfigError(err)
	r.NotNil(configErr, "must be a config error so the handler maps it to 400")
	r.Equal("username", configErr.Parameter)
}

func TestHTTPConfigNormalizeConfig_RejectsNonStringCredentials(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"username", "password"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			_, err := (&HTTPConfig{}).NormalizeConfig(map[string]any{"url": "u", key: 42})
			r.Error(err)

			configErr := checkerdef.IsConfigError(err)
			r.NotNil(configErr)
			r.Equal(key, configErr.Parameter)
		})
	}
}

// TestNormalizeConfigFor_Probe pins the optional-interface probe: a config
// that implements ConfigNormalizer is used, one that doesn't passes through.
func TestNormalizeConfigFor_Probe(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	got, err := checkerdef.NormalizeConfigFor(
		&HTTPConfig{}, map[string]any{"url": "u", "username": "alice", "password": "pw"},
	)
	r.NoError(err)
	r.Equal(map[string]any{"url": "u", "basicAuth": "alice:pw"}, got)

	in := map[string]any{"username": "alice"}
	got, err = checkerdef.NormalizeConfigFor(struct{}{}, in)
	r.NoError(err)
	r.Equal(in, got)

	got, err = checkerdef.NormalizeConfigFor(nil, in)
	r.NoError(err)
	r.Equal(in, got)
}
