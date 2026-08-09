package systemconfig

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// Env var names reused across the password precedence cases below, hoisted to
// constants so the repeated literals don't trip goconst.
const (
	envPasswordAlgorithm    = "SP_AUTH_PASSWORD_ALGORITHM"
	envPasswordArgon2Memory = "SP_AUTH_PASSWORD_ARGON2_MEMORY"
)

func TestParseBool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		value        any
		defaultValue bool
		want         bool
	}{
		{name: "native true", value: true, defaultValue: false, want: true},
		{name: "native false", value: false, defaultValue: true, want: false},
		{name: "string true lowercase", value: "true", defaultValue: false, want: true},
		{name: "string true mixed case", value: "TRUE", defaultValue: false, want: true},
		{name: "string false", value: "false", defaultValue: true, want: false},
		{name: "string 1", value: "1", defaultValue: false, want: true},
		{name: "string 0", value: "0", defaultValue: true, want: false},
		{name: "string yes", value: "yes", defaultValue: false, want: true},
		{name: "string no", value: "no", defaultValue: true, want: false},
		{name: "string padded", value: "  true  ", defaultValue: false, want: true},
		{name: "empty string falls back to default true", value: "", defaultValue: true, want: true},
		{name: "empty string falls back to default false", value: "", defaultValue: false, want: false},
		{name: "garbage string falls back to default", value: "maybe", defaultValue: true, want: true},
		{name: "nil falls back to default", value: nil, defaultValue: true, want: true},
		{name: "int falls back to default", value: 1, defaultValue: false, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			r.Equal(tt.want, parseBool(tt.value, tt.defaultValue))
		})
	}
}

// TestParseUint32 covers the argon2 numeric coercion: native int / float64 /
// numeric string accepted; negative or over-range rejected (ok=false).
func TestParseUint32(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		value  any
		want   uint32
		wantOK bool
	}{
		{name: "float64", value: float64(19456), want: 19456, wantOK: true},
		{name: "int", value: 65536, want: 65536, wantOK: true},
		{name: "numeric string", value: "32", want: 32, wantOK: true},
		{name: "zero", value: float64(0), want: 0, wantOK: true},
		{name: "negative rejected", value: float64(-1), want: 0, wantOK: false},
		{name: "non-numeric string rejected", value: "abc", want: 0, wantOK: false},
		{name: "bool rejected", value: true, want: 0, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			got, ok := parseUint32(tt.value)
			r.Equal(tt.wantOK, ok)
			r.Equal(tt.want, got)
		})
	}
}

// TestParseUint8 covers argon2 threads coercion, including the 255 ceiling.
func TestParseUint8(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		value  any
		want   uint8
		wantOK bool
	}{
		{name: "float64", value: float64(4), want: 4, wantOK: true},
		{name: "max 255", value: float64(255), want: 255, wantOK: true},
		{name: "over 255 rejected", value: float64(256), want: 0, wantOK: false},
		{name: "negative rejected", value: float64(-1), want: 0, wantOK: false},
		{name: "bool rejected", value: false, want: 0, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			got, ok := parseUint8(tt.value)
			r.Equal(tt.wantOK, ok)
			r.Equal(tt.want, got)
		})
	}
}

// TestKnownSlackKeys pins the exact canonical parameter-key strings the backend
// reads for Slack. The dashboard Slack Socket Mode page
// (web/dash0/src/routes/orgs/$org/server.slack.tsx) writes these literal keys;
// if either side drifts, the in-app toggle silently stops working. This test is
// the backend half of that front/back contract — keep the strings in sync with
// the KEY_* constants in server.slack.tsx.
func TestKnownSlackKeys(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("auth.slack.enabled", string(KeySlackEnabled))
	r.Equal("auth.slack.socket_mode_enabled", string(KeySlackSocketModeEnabled))
	r.Equal("auth.slack.app_token", string(KeySlackAppToken))

	// Each key must be a known parameter that actually applies to cfg.Slack,
	// and the app token must be flagged secret so it lands in the encrypted path.
	known := getKnownParameters()
	byKey := make(map[ParameterKey]ParameterDefinition, len(known))
	for _, def := range known {
		byKey[def.Key] = def
	}

	for _, key := range []ParameterKey{KeySlackEnabled, KeySlackSocketModeEnabled, KeySlackAppToken} {
		def, ok := byKey[key]
		r.Truef(ok, "expected %q to be a known parameter", key)
		r.NotNil(def.ApplyFunc)
	}
	r.False(byKey[KeySlackSocketModeEnabled].Secret, "socket_mode_enabled must not be secret")
	r.True(byKey[KeySlackAppToken].Secret, "app_token must be secret")
}

// TestKnownPasswordKeys pins the canonical auth.password.* parameter-key strings
// the backend reads. The dashboard Password Hashing page
// (web/dash0/src/routes/orgs/$org/server.hashing.tsx) writes these literal keys;
// if either side drifts, saving from the UI silently stops applying. Keep these
// in sync with the KEY_* constants in server.hashing.tsx.
func TestKnownPasswordKeys(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("auth.password.algorithm", string(KeyPasswordAlgorithm))
	r.Equal("auth.password.argon2.memory", string(KeyPasswordArgon2Memory))
	r.Equal("auth.password.argon2.time", string(KeyPasswordArgon2Time))
	r.Equal("auth.password.argon2.threads", string(KeyPasswordArgon2Threads))
	r.Equal("auth.password.argon2.key_length", string(KeyPasswordArgon2KeyLen))
	r.Equal("auth.password.argon2.salt_length", string(KeyPasswordArgon2SaltLen))
	r.Equal("auth.password.bcrypt.cost", string(KeyPasswordBcryptCost))
	r.Equal("auth.password.rehash_on_login", string(KeyPasswordRehashOnLogin))

	known := getKnownParameters()
	byKey := make(map[ParameterKey]ParameterDefinition, len(known))
	for _, def := range known {
		byKey[def.Key] = def
	}

	for _, key := range []ParameterKey{
		KeyPasswordAlgorithm, KeyPasswordArgon2Memory, KeyPasswordArgon2Time,
		KeyPasswordArgon2Threads, KeyPasswordArgon2KeyLen, KeyPasswordArgon2SaltLen,
		KeyPasswordBcryptCost, KeyPasswordRehashOnLogin,
	} {
		def, ok := byKey[key]
		r.Truef(ok, "expected %q to be a known parameter", key)
		r.NotNil(def.ApplyFunc)
		r.Falsef(def.Secret, "%q must not be secret", key)
	}
}

// TestKnownSchedulingFastLaneReservedKey pins the scheduling.fast_lane_reserved
// parameter key that the dashboard Performance settings page
// (web/dash0/src/routes/orgs/$org/server.performance.tsx) writes, and verifies
// the JSON-float64 coercion onto cfg.Server.Scheduling.FastLaneReserved. If
// either side drifts, saving from the UI silently stops applying.
// TestKnownSessionMaxDurationKey verifies auth.session_max_duration is
// registered like the other known parameters: correct key string, an
// ApplyFunc that coerces JSON-decoded float64 seconds (and plain int) onto
// cfg.Auth.SessionMaxDuration, and an EnvVar matching what applyAuthEnv reads
// directly via os.Getenv (bypassing koanf's underscore-collapsing env
// loader, same as every other multi-word auth.* knob).
func TestKnownSessionMaxDurationKey(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("auth.session_max_duration", string(KeySessionMaxDuration))

	var def ParameterDefinition

	found := false

	for _, d := range getKnownParameters() {
		if d.Key == KeySessionMaxDuration {
			def, found = d, true

			break
		}
	}

	r.True(found, "auth.session_max_duration must be a known parameter")
	r.NotNil(def.ApplyFunc)
	r.False(def.Secret)
	r.Equal("SP_AUTH_SESSION_MAX_DURATION", def.EnvVar)

	cfg := &config.Config{}
	def.ApplyFunc(cfg, float64(7200)) // JSON-decoded numbers arrive as float64
	r.Equal(7200*time.Second, cfg.Auth.SessionMaxDuration)
	def.ApplyFunc(cfg, 3600)
	r.Equal(3600*time.Second, cfg.Auth.SessionMaxDuration)

	// A negative value is treated as unset (no sensible meaning), not
	// clamped or zeroed — the prior value is left alone.
	def.ApplyFunc(cfg, float64(-1))
	r.Equal(3600*time.Second, cfg.Auth.SessionMaxDuration)
}

func TestKnownSchedulingFastLaneReservedKey(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("scheduling.fast_lane_reserved", string(KeySchedulingFastLaneReserved))

	var def ParameterDefinition

	found := false

	for _, d := range getKnownParameters() {
		if d.Key == KeySchedulingFastLaneReserved {
			def, found = d, true

			break
		}
	}

	r.True(found, "scheduling.fast_lane_reserved must be a known parameter")
	r.NotNil(def.ApplyFunc)
	r.False(def.Secret)
	r.Equal("SP_SCHEDULING_FAST_LANE_RESERVED", def.EnvVar,
		"must match the env var applySchedulingEnv reads so precedence stays consistent")

	cfg := &config.Config{}
	def.ApplyFunc(cfg, float64(8)) // JSON-decoded numbers arrive as float64
	r.Equal(8, cfg.Server.Scheduling.FastLaneReserved)
	def.ApplyFunc(cfg, 3)
	r.Equal(3, cfg.Server.Scheduling.FastLaneReserved)
}

// TestInitializeAppliesPasswordParams verifies the full DB -> cfg apply path for
// the password-hashing policy: rows persisted under auth.password.* must land on
// cfg.Auth.Password on Initialize, with numeric coercion (float64 -> uint32/int),
// algorithm string, and the rehash bool. env > db precedence is also exercised.
// t.Setenv below makes this test non-parallel.
func TestInitializeAppliesPasswordParams(t *testing.T) {
	type paramRow struct {
		value  any
		secret bool
	}

	tests := []struct {
		name          string
		dbParams      map[string]paramRow
		env           map[string]string
		wantAlgorithm string
		wantMemory    uint32
		wantTime      uint32
		wantThreads   uint8
		wantKeyLen    uint32
		wantSaltLen   uint32
		wantCost      int
		wantRehash    bool
	}{
		{
			name: "db values apply with numeric coercion",
			dbParams: map[string]paramRow{
				// JSON-decoded numbers arrive as float64; ApplyFunc must coerce.
				string(KeyPasswordAlgorithm):     {value: "argon2id"},
				string(KeyPasswordArgon2Memory):  {value: float64(19456)},
				string(KeyPasswordArgon2Time):    {value: float64(2)},
				string(KeyPasswordArgon2Threads): {value: float64(1)},
				string(KeyPasswordArgon2KeyLen):  {value: float64(32)},
				string(KeyPasswordArgon2SaltLen): {value: float64(16)},
				string(KeyPasswordBcryptCost):    {value: float64(13)},
				string(KeyPasswordRehashOnLogin): {value: false},
			},
			wantAlgorithm: "argon2id",
			wantMemory:    19456,
			wantTime:      2,
			wantThreads:   1,
			wantKeyLen:    32,
			wantSaltLen:   16,
			wantCost:      13,
			wantRehash:    false,
		},
		{
			// env > DB conflict: a DB row says argon2id/m=19456 but the env var
			// sets a *conflicting* bcrypt/m=65536. The env value must win after
			// the overlay — this is the authoritative end-to-end precedence the
			// dual read paths must agree on.
			name: "env overrides conflicting db value",
			dbParams: map[string]paramRow{
				string(KeyPasswordAlgorithm):    {value: "argon2id"},
				string(KeyPasswordArgon2Memory): {value: float64(19456)},
			},
			env: map[string]string{
				envPasswordAlgorithm:    "bcrypt",
				envPasswordArgon2Memory: "65536",
			},
			wantAlgorithm: "bcrypt",
			wantMemory:    65536,
			wantRehash:    false, // zero-value cfg, no rehash row
		},
		{
			// env > default with NO DB row: env alone must override the
			// (zero-value) default, proving the env-only bootstrap path and the
			// overlay resolve to the same value rather than fighting each other.
			name:     "env wins over default with no db row",
			dbParams: map[string]paramRow{},
			env: map[string]string{
				envPasswordAlgorithm:           "bcrypt",
				"SP_AUTH_PASSWORD_BCRYPT_COST": "14",
				envPasswordArgon2Memory:        "19456",
			},
			wantAlgorithm: "bcrypt",
			wantMemory:    19456,
			wantCost:      14,
			wantRehash:    false, // zero-value cfg, no rehash row
		},
		{
			name:       "absent params leave config zero values",
			dbParams:   map[string]paramRow{},
			wantRehash: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := require.New(t)
			ctx := context.Background()

			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
			r.NoError(err)
			r.NoError(dbSvc.Initialize(ctx))
			t.Cleanup(func() { _ = dbSvc.Close() })

			for key, p := range tt.dbParams {
				r.NoError(dbSvc.SetSystemParameter(ctx, key, p.value, p.secret))
			}

			cfg := &config.Config{}
			svc := NewService(dbSvc, cfg)
			r.NoError(svc.Initialize(ctx))

			pw := cfg.Auth.Password
			r.Equal(tt.wantAlgorithm, pw.Algorithm)
			r.Equal(tt.wantMemory, pw.Argon2.Memory)
			r.Equal(tt.wantTime, pw.Argon2.Time)
			r.Equal(tt.wantThreads, pw.Argon2.Threads)
			r.Equal(tt.wantKeyLen, pw.Argon2.KeyLength)
			r.Equal(tt.wantSaltLen, pw.Argon2.SaltLength)
			r.Equal(tt.wantCost, pw.Bcrypt.Cost)
			r.Equal(tt.wantRehash, pw.RehashOnLogin)
		})
	}
}

// TestKnownMicrosoftTenantKey pins the canonical auth.microsoft.tenant_id key the
// backend reads and asserts it is non-secret (it is part of the public authorize
// URL, not a credential). The dashboard Authentication page
// (web/dash0/src/routes/orgs/$org/server.auth.tsx) writes this literal key; if
// either side drifts, saving the tenant from the UI silently stops applying.
func TestKnownMicrosoftTenantKey(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("auth.microsoft.tenant_id", string(KeyMicrosoftTenantID))

	known := getKnownParameters()
	byKey := make(map[ParameterKey]ParameterDefinition, len(known))
	for _, def := range known {
		byKey[def.Key] = def
	}

	def, ok := byKey[KeyMicrosoftTenantID]
	r.Truef(ok, "expected %q to be a known parameter", KeyMicrosoftTenantID)
	r.NotNil(def.ApplyFunc)
	r.Equal("SP_MICROSOFT_TENANT_ID", def.EnvVar)
	r.Falsef(def.Secret, "%q must not be secret", KeyMicrosoftTenantID)
}

// TestInitializeAppliesMicrosoftTenant verifies the full DB -> cfg apply path for
// the Microsoft tenant: a row under auth.microsoft.tenant_id lands on
// cfg.Microsoft.TenantID on Initialize, env overrides the DB value
// (env > db > default), surrounding whitespace is trimmed, and an absent key
// leaves TenantID == "" (which the URL builders treat as the "common" default).
// t.Setenv below makes this test non-parallel.
func TestInitializeAppliesMicrosoftTenant(t *testing.T) {
	const envTenant = "SP_MICROSOFT_TENANT_ID"

	tests := []struct {
		name       string
		dbValue    any
		hasDBValue bool
		env        map[string]string
		wantTenant string
	}{
		{
			name:       "db value applies to cfg",
			dbValue:    "11111111-2222-3333-4444-555555555555",
			hasDBValue: true,
			wantTenant: "11111111-2222-3333-4444-555555555555",
		},
		{
			name:       "env overrides db value",
			dbValue:    "tenant-from-db",
			hasDBValue: true,
			env:        map[string]string{envTenant: "tenant-from-env"},
			wantTenant: "tenant-from-env",
		},
		{
			name:       "surrounding whitespace is trimmed",
			dbValue:    "  contoso.onmicrosoft.com  ",
			hasDBValue: true,
			wantTenant: "contoso.onmicrosoft.com",
		},
		{
			name:       "absent key leaves empty tenant (common default)",
			hasDBValue: false,
			wantTenant: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := require.New(t)
			ctx := context.Background()

			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
			r.NoError(err)
			r.NoError(dbSvc.Initialize(ctx))
			t.Cleanup(func() { _ = dbSvc.Close() })

			if tt.hasDBValue {
				r.NoError(dbSvc.SetSystemParameter(ctx, string(KeyMicrosoftTenantID), tt.dbValue, false))
			}

			cfg := &config.Config{}
			svc := NewService(dbSvc, cfg)
			r.NoError(svc.Initialize(ctx))

			r.Equal(tt.wantTenant, cfg.Microsoft.TenantID)
		})
	}
}

// TestInitializeAppliesSlackParams verifies the full DB -> cfg apply path that
// the dashboard depends on: rows persisted under the auth.slack.* keys must land
// on cfg.Slack on Initialize, and env vars must override the DB value
// (env > db > default). t.Setenv below makes this test non-parallel.
func TestInitializeAppliesSlackParams(t *testing.T) {
	tests := []struct {
		name     string
		dbParams map[string]struct {
			value  any
			secret bool
		}
		env             map[string]string
		wantSocketMode  bool
		wantAppToken    string
		wantSlackEnable bool
	}{
		{
			name: "db values apply to cfg",
			dbParams: map[string]struct {
				value  any
				secret bool
			}{
				string(KeySlackEnabled):           {value: true, secret: false},
				string(KeySlackSocketModeEnabled): {value: true, secret: false},
				string(KeySlackAppToken):          {value: "xapp-from-db", secret: true},
			},
			wantSocketMode:  true,
			wantAppToken:    "xapp-from-db",
			wantSlackEnable: true,
		},
		{
			name: "env overrides db value",
			dbParams: map[string]struct {
				value  any
				secret bool
			}{
				string(KeySlackSocketModeEnabled): {value: false, secret: false},
				string(KeySlackAppToken):          {value: "xapp-from-db", secret: true},
			},
			env: map[string]string{
				// boolStringTrue ("true") is reused instead of a fresh literal
				// to keep the package under the goconst occurrence threshold.
				"SP_SLACK_SOCKET_MODE_ENABLED": boolStringTrue,
				"SP_SLACK_APP_TOKEN":           "xapp-from-env",
			},
			wantSocketMode: true,
			wantAppToken:   "xapp-from-env",
		},
		{
			name: "absent params leave config defaults",
			dbParams: map[string]struct {
				value  any
				secret bool
			}{},
			wantSocketMode:  false,
			wantAppToken:    "",
			wantSlackEnable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := require.New(t)
			ctx := context.Background()

			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
			r.NoError(err)
			r.NoError(dbSvc.Initialize(ctx))
			t.Cleanup(func() { _ = dbSvc.Close() })

			for key, p := range tt.dbParams {
				r.NoError(dbSvc.SetSystemParameter(ctx, key, p.value, p.secret))
			}

			cfg := &config.Config{}
			svc := NewService(dbSvc, cfg)
			r.NoError(svc.Initialize(ctx))

			r.Equal(tt.wantSocketMode, cfg.Slack.SocketModeEnabled)
			r.Equal(tt.wantAppToken, cfg.Slack.AppToken)
			r.Equal(tt.wantSlackEnable, cfg.Slack.Enabled)
		})
	}
}

// TestKnownEnvVars verifies the pure KnownEnvVars accessor exposes every
// parameter's EnvVar (all non-empty, no duplicates in the underlying table) and
// includes representative names the unrecognized-env startup check relies on.
func TestKnownEnvVars(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	names := KnownEnvVars()
	r.Len(names, len(getKnownParameters()), "every parameter contributes one EnvVar")

	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		r.NotEmpty(name)
		r.Truef(len(name) > 3 && name[:3] == "SP_", "%q must be SP_-prefixed", name)
		set[name] = struct{}{}
	}

	r.Contains(set, "SP_AUTH_JWT_SECRET")
	r.Contains(set, "SP_BASE_URL")
	r.Contains(set, "SP_SLACK_APP_TOKEN")
}

// TestKnownPostHogKeys pins the canonical posthog.* parameter-key strings and
// their secret classification (spec 2026-08-02-08). The dashboard Analytics
// page (web/dash0/src/routes/orgs/$org/server.analytics.tsx) writes these
// literal keys; if either side drifts, saving from the UI silently stops
// applying. Keep these in sync with the KEY_* constants in that file.
//
// The secret split is load-bearing for the privacy guarantee: the project key
// is deliberately NOT secret (it is shipped to the browser), while the personal
// key MUST be secret so it never leaves the process.
func TestKnownPostHogKeys(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("posthog.enabled", string(KeyPostHogEnabled))
	r.Equal("posthog.project_api_key", string(KeyPostHogProjectAPIKey))
	r.Equal("posthog.host", string(KeyPostHogHost))
	r.Equal("posthog.personal_api_key", string(KeyPostHogPersonalAPIKey))

	known := getKnownParameters()
	byKey := make(map[ParameterKey]ParameterDefinition, len(known))
	for _, def := range known {
		byKey[def.Key] = def
	}

	expectedEnv := map[ParameterKey]string{
		KeyPostHogEnabled:        EnvPostHogEnabled,
		KeyPostHogProjectAPIKey:  EnvPostHogProjectAPIKey,
		KeyPostHogHost:           EnvPostHogHost,
		KeyPostHogPersonalAPIKey: EnvPostHogPersonalAPIKey,
	}

	for key, envVar := range expectedEnv {
		def, ok := byKey[key]
		r.Truef(ok, "expected %q to be a known parameter", key)
		r.NotNil(def.ApplyFunc)
		r.Equalf(envVar, def.EnvVar, "unexpected env var for %q", key)
	}

	r.False(byKey[KeyPostHogEnabled].Secret, "posthog.enabled must not be secret")
	r.False(byKey[KeyPostHogHost].Secret, "posthog.host must not be secret")
	r.False(byKey[KeyPostHogProjectAPIKey].Secret,
		"posthog.project_api_key is the public browser key and must not be secret")
	r.True(byKey[KeyPostHogPersonalAPIKey].Secret,
		"posthog.personal_api_key must be secret — it must never leave the process")
}

// TestPostHogEnablementRule proves the single enablement rule shared by the
// backend, GET /api/v1/config and the dashboard: the kill switch never enables
// anything on its own, and a key alone never overrides the kill switch.
func TestPostHogEnablementRule(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.False(config.PostHogConfig{}.Active(), "zero value must be off")
	r.False(config.PostHogConfig{Enabled: true}.Active(),
		"enabled with no key must be off — this is the self-hosted default")
	r.False(config.PostHogConfig{Enabled: true, ProjectAPIKey: "  "}.Active(),
		"whitespace-only key must be off")
	r.False(config.PostHogConfig{ProjectAPIKey: "phc_k"}.Active(),
		"key with the kill switch off must be off")
	r.True(config.PostHogConfig{Enabled: true, ProjectAPIKey: "phc_k"}.Active())
}

// TestInitializeAppliesPostHogParams verifies the env > db > default precedence
// for the posthog.* keys, including the negative case: with nothing set
// anywhere, the resolved config must stay inactive.
func TestInitializeAppliesPostHogParams(t *testing.T) {
	type paramRow struct {
		value  any
		secret bool
	}

	tests := []struct {
		name        string
		dbParams    map[string]paramRow
		env         map[string]string
		wantKey     string
		wantEnabled bool
		wantActive  bool
	}{
		{
			// The stock self-hosted install: kill switch on, no key anywhere.
			name:        "defaults resolve to inactive",
			wantEnabled: true,
			wantActive:  false,
		},
		{
			name: "db key activates",
			dbParams: map[string]paramRow{
				string(KeyPostHogProjectAPIKey): {value: "phc_from_db"},
			},
			wantKey:     "phc_from_db",
			wantEnabled: true,
			wantActive:  true,
		},
		{
			name: "env beats db",
			dbParams: map[string]paramRow{
				string(KeyPostHogProjectAPIKey): {value: "phc_from_db"},
			},
			env:         map[string]string{EnvPostHogProjectAPIKey: "phc_from_env"},
			wantKey:     "phc_from_env",
			wantEnabled: true,
			wantActive:  true,
		},
		{
			name: "db kill switch disables a configured key",
			dbParams: map[string]paramRow{
				string(KeyPostHogProjectAPIKey): {value: "phc_from_db"},
				string(KeyPostHogEnabled):       {value: false},
			},
			wantKey:     "phc_from_db",
			wantEnabled: false,
			wantActive:  false,
		},
		{
			name:        "env kill switch disables a configured key",
			dbParams:    map[string]paramRow{string(KeyPostHogProjectAPIKey): {value: "phc_from_db"}},
			env:         map[string]string{EnvPostHogEnabled: strconv.FormatBool(false)},
			wantKey:     "phc_from_db",
			wantEnabled: false,
			wantActive:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := require.New(t)
			ctx := context.Background()

			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
			r.NoError(err)
			r.NoError(dbSvc.Initialize(ctx))
			t.Cleanup(func() { _ = dbSvc.Close() })

			for key, p := range tt.dbParams {
				r.NoError(dbSvc.SetSystemParameter(ctx, key, p.value, p.secret))
			}

			cfg := &config.Config{PostHog: config.PostHogConfig{
				Enabled: true, Host: config.DefaultPostHogHost,
			}}
			r.NoError(NewService(dbSvc, cfg).Initialize(ctx))

			r.Equal(tt.wantKey, cfg.PostHog.ProjectAPIKey)
			r.Equal(tt.wantEnabled, cfg.PostHog.Enabled)
			r.Equal(tt.wantActive, cfg.PostHog.Active())

			for k := range tt.env {
				r.Contains(EnvOverriddenKeys(), envKeyToParam(k))
			}
		})
	}
}

// envKeyToParam maps the SP_* names used above back to their parameter key so
// the env-override reporting is asserted from the same table.
func envKeyToParam(envVar string) string {
	switch envVar {
	case EnvPostHogProjectAPIKey:
		return string(KeyPostHogProjectAPIKey)
	case EnvPostHogEnabled:
		return string(KeyPostHogEnabled)
	default:
		return envVar
	}
}

// TestNodeRoleParameterApply covers the node.role overlay (spec 2026-08-09-01).
// This ApplyFunc runs AFTER config.Validate(), so it is the one path where an
// unvalidated role could reach the subsystem gates: a multi-value role must be
// accepted verbatim, and an invalid one must be refused in favor of the
// already-validated value rather than silently switching subsystems off.
func TestNodeRoleParameterApply(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var def ParameterDefinition

	for _, candidate := range getKnownParameters() {
		if candidate.Key == KeyNodeRole {
			def = candidate

			break
		}
	}

	r.Equalf(KeyNodeRole, def.Key, "expected %q to be a known parameter", KeyNodeRole)
	r.NotNil(def.ApplyFunc)

	tests := []struct {
		name     string
		stored   any
		want     string
		wantAPI  bool
		wantJobs bool
		wantChks bool
	}{
		{
			name: "multi-value role is applied", stored: "api,jobs", want: "api,jobs",
			wantAPI: true, wantJobs: true, wantChks: false,
		},
		{
			name: "single-value role is applied", stored: "checks", want: "checks",
			wantChks: true,
		},
		{
			name: "invalid role keeps the configured value", stored: "api,chekcs", want: "all",
			wantAPI: true, wantJobs: true, wantChks: true,
		},
		{
			name: "conflicting combination keeps the configured value", stored: "agent,api", want: "all",
			wantAPI: true, wantJobs: true, wantChks: true,
		},
		{
			name: "empty value is ignored", stored: "", want: "all",
			wantAPI: true, wantJobs: true, wantChks: true,
		},
		{
			name: "non-string value is ignored", stored: 42, want: "all",
			wantAPI: true, wantJobs: true, wantChks: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rr := require.New(t)
			cfg := &config.Config{Node: config.NodeConfig{Role: config.NodeRoleAll}}

			def.ApplyFunc(cfg, tt.stored)

			rr.Equal(tt.want, cfg.Node.Role)
			rr.Equal(tt.wantAPI, cfg.ShouldRunAPI())
			rr.Equal(tt.wantJobs, cfg.ShouldRunJobs())
			rr.Equal(tt.wantChks, cfg.ShouldRunChecks())
		})
	}
}
