package config_test

import (
	"fmt"
	"testing"
	"time"

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

func TestAuthFromMapRejectsWrongTypes(t *testing.T) {
	t.Parallel()

	for name, in := range map[string]map[string]any{
		"require_spf not a string":    {"require_spf": 1},
		"require_dmarc not a string":  {"require_dmarc": true},
		"require_dkim not a bool":     {"require_dkim": "yes"},
		"check_blocklists not a bool": {"check_blocklists": 1},
		"blocklists not a list":       {"blocklists": "zen.spamhaus.org"},
		"blocklists item not string":  {"blocklists": []any{"zen.spamhaus.org", 3}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.EmailConfig{}
			require.Error(t, cfg.FromMap(in))
		})
	}
}

func TestAuthFromMapAcceptsStringSlice(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &config.EmailConfig{}
	r.NoError(cfg.FromMap(map[string]any{"blocklists": []string{"a.test"}}))
	r.Equal([]string{"a.test"}, cfg.Blocklists)
	r.True(cfg.BlocklistsEnabled())
}

func TestFromMapRejectsNonStringToken(t *testing.T) {
	t.Parallel()

	cfg := &config.EmailConfig{}
	require.Error(t, cfg.FromMap(map[string]any{"token": 42}))
}

func TestMaxDeliveryDelayRoundTrip(t *testing.T) {
	t.Parallel()

	cfg := &config.EmailConfig{}
	require.NoError(t, cfg.FromMap(map[string]any{"max_delivery_delay": "10m"}))
	require.Equal(t, 10*time.Minute, cfg.MaxDeliveryDelay)
	require.Equal(t, "10m", cfg.GetConfig()["max_delivery_delay"])

	absent := &config.EmailConfig{}
	require.NoError(t, absent.FromMap(map[string]any{"token": "x"}))
	require.NotContains(t, absent.GetConfig(), "max_delivery_delay")
}

func TestValidateSpecMaxDeliveryDelay(t *testing.T) {
	t.Parallel()

	for _, bad := range []any{"soon", 10, "-5m", "30s", "2h"} {
		spec := &checkerdef.CheckSpec{Period: time.Hour, Config: map[string]any{"max_delivery_delay": bad}}
		require.Error(t, config.ValidateSpec(spec), "%v", bad)
	}

	spec := &checkerdef.CheckSpec{Period: time.Hour, Config: map[string]any{"max_delivery_delay": "10m"}}
	require.NoError(t, config.ValidateSpec(spec))

	unknownPeriod := &checkerdef.CheckSpec{Config: map[string]any{"max_delivery_delay": "2h"}}
	require.NoError(t, config.ValidateSpec(unknownPeriod))
}

func TestFormatDuration(t *testing.T) {
	t.Parallel()

	cases := map[time.Duration]string{
		10 * time.Minute:                 "10m",
		time.Hour:                        "1h",
		90 * time.Minute:                 "1h30m",
		2*time.Hour + 30*time.Second:     "2h0m30s",
		45 * time.Second:                 "45s",
		24 * time.Hour:                   "24h",
		time.Hour + 5*time.Minute:        "1h5m",
		3*time.Hour + 20*time.Minute:     "3h20m",
		time.Minute + 30*time.Second:     "1m30s",
		time.Hour + 59*time.Minute:       "1h59m",
		5*time.Hour + 0*time.Millisecond: "5h",
	}

	for in, want := range cases {
		require.Equal(t, want, config.FormatDuration(in), "%v", in)
	}
}

func TestEmptyMaxDeliveryDelayMeansNoLimit(t *testing.T) {
	t.Parallel()

	cfg := &config.EmailConfig{}
	require.NoError(t, cfg.FromMap(map[string]any{"max_delivery_delay": ""}))
	require.Zero(t, cfg.MaxDeliveryDelay)
}

func TestValidateDeliveryDelayForPeriod(t *testing.T) {
	t.Parallel()

	for _, bad := range []any{"soon", 10, "30s", "-5m"} {
		require.Error(t,
			config.ValidateDeliveryDelayForPeriod(map[string]any{"max_delivery_delay": bad}, time.Hour), "%v", bad)
	}

	require.Error(t, config.ValidateDeliveryDelayForPeriod(map[string]any{"max_delivery_delay": "1h"}, time.Hour))
	require.NoError(t, config.ValidateDeliveryDelayForPeriod(map[string]any{"max_delivery_delay": "10m"}, time.Hour))
	require.NoError(t, config.ValidateDeliveryDelayForPeriod(map[string]any{"max_delivery_delay": ""}, time.Hour))
	require.NoError(t, config.ValidateDeliveryDelayForPeriod(map[string]any{}, time.Hour))
}
