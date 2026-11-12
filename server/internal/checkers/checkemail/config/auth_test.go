package config_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkemail/config"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestAuthConfigRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	in := map[string]any{
		"token":            "abc",
		"require_spf":      "softfail",
		"require_dkim":     true,
		"require_dmarc":    "pass",
		"check_blocklists": true,
		"blocklists":       []any{"zen.spamhaus.org", "bl.spamcop.net"},
	}

	cfg := &config.EmailConfig{}
	r.NoError(cfg.FromMap(in))
	r.True(cfg.AuthEnabled())

	out := cfg.GetConfig()
	r.Equal("softfail", out["require_spf"])
	r.Equal(true, out["require_dkim"])
	r.Equal("pass", out["require_dmarc"])
	r.Equal(true, out["check_blocklists"])
	r.Equal([]string{"zen.spamhaus.org", "bl.spamcop.net"}, out["blocklists"])

	again := &config.EmailConfig{}
	r.NoError(again.FromMap(out))
	r.Equal(cfg, again)
}

func TestAuthConfigAbsentAddsNoKey(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &config.EmailConfig{}
	r.NoError(cfg.FromMap(map[string]any{"token": "abc"}))
	r.False(cfg.AuthEnabled())
	r.Equal(map[string]any{"token": "abc"}, cfg.GetConfig())
}

func TestBlocklistsImplyCheck(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &config.EmailConfig{Blocklists: []string{"zen.spamhaus.org"}}
	r.True(cfg.BlocklistsEnabled())
	r.True(cfg.AuthEnabled())
}

func TestSPFAccepts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		require, verdict string
		want             bool
	}{
		{"pass", "pass", true},
		{"pass", "softfail", false},
		{"softfail", "neutral", true},
		{"softfail", "softfail", true},
		{"softfail", "fail", false},
		{"none", "none", true},
		{"none", "fail", false},
		{"none", "permerror", false},
		{"", "fail", true},
	}

	for _, tt := range tests {
		cfg := &config.EmailConfig{RequireSPF: tt.require}
		require.Equal(t, tt.want, cfg.SPFAccepts(tt.verdict), "%s vs %s", tt.require, tt.verdict)
	}
}

func TestValidateSpecAuth(t *testing.T) {
	t.Parallel()

	bad := []map[string]any{
		{"require_spf": "strict"},
		{"require_dmarc": "quarantine"},
		{"require_dkim": "yes"},
		{"blocklists": []any{"not a zone"}},
		{"blocklists": []any{"localhost"}},
		{"blocklists": "zen.spamhaus.org"},
		{"blocklists": tooManyZones()},
	}

	for _, c := range bad {
		spec := &checkerdef.CheckSpec{Config: c}
		require.Error(t, config.ValidateSpec(spec), "%v", c)
	}

	spec := &checkerdef.CheckSpec{Config: map[string]any{
		"require_spf": "pass", "blocklists": []any{"zen.spamhaus.org"},
	}}
	require.NoError(t, config.ValidateSpec(spec))

	plain := &checkerdef.CheckSpec{}
	require.NoError(t, config.ValidateSpec(plain))
	require.Len(t, plain.Config, 1, "only the generated token")
}

func tooManyZones() []any {
	zones := make([]any, 0, 17)
	for i := range 17 {
		zones = append(zones, fmt.Sprintf("zone%d.example.com", i))
	}

	return zones
}
