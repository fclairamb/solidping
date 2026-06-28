package systemconfig

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
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

// TestInitializeAppliesSlackParams verifies the full DB -> cfg apply path that
// the dashboard depends on: rows persisted under the auth.slack.* keys must land
// on cfg.Slack on Initialize, and env vars must override the DB value
// (env > db > default).
//
//nolint:paralleltest // env-precedence case uses t.Setenv (process-global env)
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
				"SP_SLACK_SOCKET_MODE_ENABLED": "true",
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
		t.Run(tt.name, func(t *testing.T) { //nolint:paralleltest // mutates process-global env via t.Setenv
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
