package systemconfig

import (
	"context"
	"testing"

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
