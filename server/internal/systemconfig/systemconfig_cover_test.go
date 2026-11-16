package systemconfig

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// TestApplyFuncsAcceptEveryValueShape drives every parameter's ApplyFunc with the
// value shapes the parameters table can hand it (JSON-decoded strings, numbers,
// bools, and garbage). None may panic, and the garbage must never be applied.
func TestApplyFuncsAcceptEveryValueShape(t *testing.T) {
	t.Parallel()

	values := []any{
		"x", "", "true", "false", "1", "42", "30s", "a,b", "https://acme.com",
		float64(0), float64(7), float64(1.5), 3, int64(5), true, false,
		nil,
		[]any{"a", "b"},
		[]string{"c"},
		map[string]any{"k": "v"},
	}

	for _, def := range getKnownParameters() {
		if def.ApplyFunc == nil {
			continue
		}

		t.Run(string(def.Key), func(t *testing.T) {
			t.Parallel()

			for _, v := range values {
				cfg := &config.Config{}
				require.NotPanics(t, func() { def.ApplyFunc(cfg, v) }, "value %#v", v)
			}
		})
	}
}

func TestIsAggregationRetentionKey(t *testing.T) {
	t.Parallel()

	require.True(t, IsAggregationRetentionKey(string(KeyPerfAggRetentionRawHours)))
	require.True(t, IsAggregationRetentionKey(string(KeyPerfAggRetentionHourDays)))
	require.True(t, IsAggregationRetentionKey(string(KeyPerfAggRetentionDayMonths)))
	require.False(t, IsAggregationRetentionKey("auth.jwt_secret"))
}

func TestValidateAggregationRetentionParameter(t *testing.T) {
	t.Parallel()

	key := string(KeyPerfAggRetentionRawHours)
	tests := []struct {
		name    string
		key     string
		value   any
		wantErr error
	}{
		{"other key ignored", "auth.jwt_secret", "junk", nil},
		{"int", key, 5, nil},
		{"int64", key, int64(5), nil},
		{"float whole", key, float64(24), nil},
		{"float32 whole", key, float32(3), nil},
		{"string number", key, " 12 ", nil},
		{"float fractional", key, 1.5, errRetentionNotInteger},
		{"float32 fractional", key, float32(1.5), errRetentionNotInteger},
		{"string junk", key, "abc", errRetentionNotInteger},
		{"bool", key, true, errRetentionNotInteger},
		{"zero", key, 0, errRetentionBelowFloor},
		{"negative", key, float64(-3), errRetentionBelowFloor},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateAggregationRetentionParameter(tt.key, tt.value)
			if tt.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
}

func newCoverService(t *testing.T) (*Service, context.Context, interface {
	SetSystemParameter(ctx context.Context, key string, value any, secret bool) error
},
) {
	t.Helper()

	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))

	t.Cleanup(func() { _ = dbSvc.Close() })

	return NewService(dbSvc, &config.Config{}), ctx, dbSvc
}

func TestServiceTypedGetters(t *testing.T) {
	svc, ctx, dbSvc := newCoverService(t)

	const key ParameterKey = "usr.cover_test"

	// Defaults when nothing is stored.
	s, err := svc.GetString(ctx, key, "SP_COVER_TEST_ENV", "dflt")
	require.NoError(t, err)
	require.Equal(t, "dflt", s)

	n, err := svc.GetInt(ctx, key, "SP_COVER_TEST_ENV", 9)
	require.NoError(t, err)
	require.Equal(t, 9, n)

	b, err := svc.GetBool(ctx, key, "SP_COVER_TEST_ENV", true)
	require.NoError(t, err)
	require.True(t, b)

	// Stored values.
	require.NoError(t, dbSvc.SetSystemParameter(ctx, string(key), "stored", false))
	s, err = svc.GetString(ctx, key, "SP_COVER_TEST_ENV", "dflt")
	require.NoError(t, err)
	require.Equal(t, "stored", s)

	require.NoError(t, dbSvc.SetSystemParameter(ctx, string(key), 12, false))
	n, err = svc.GetInt(ctx, key, "SP_COVER_TEST_ENV", 9)
	require.NoError(t, err)
	require.Equal(t, 12, n)

	require.NoError(t, dbSvc.SetSystemParameter(ctx, string(key), false, false))
	b, err = svc.GetBool(ctx, key, "SP_COVER_TEST_ENV", true)
	require.NoError(t, err)
	require.False(t, b)

	// Environment wins over the database.
	t.Setenv("SP_COVER_TEST_ENV", "yes")

	b, err = svc.GetBool(ctx, key, "SP_COVER_TEST_ENV", false)
	require.NoError(t, err)
	require.True(t, b)

	t.Setenv("SP_COVER_TEST_ENV", "77")

	n, err = svc.GetInt(ctx, key, "SP_COVER_TEST_ENV", 9)
	require.NoError(t, err)
	require.Equal(t, 77, n)

	s, err = svc.GetString(ctx, key, "SP_COVER_TEST_ENV", "dflt")
	require.NoError(t, err)
	require.Equal(t, "77", s)
}

func TestEnsureJWTSecret(t *testing.T) {
	svc, ctx, _ := newCoverService(t)

	// Generates and persists a secret, then reuses it.
	require.NoError(t, svc.ensureJWTSecret(ctx))
	first := svc.config.Auth.JWTSecret
	require.NotEmpty(t, first)
	require.NotEqual(t, "change-me-in-production", first)

	// Explicit non-default secret is kept as-is.
	require.NoError(t, svc.ensureJWTSecret(ctx))
	require.Equal(t, first, svc.config.Auth.JWTSecret)

	// Reload from the database when the config holds the placeholder.
	svc.config.Auth.JWTSecret = "change-me-in-production"
	require.NoError(t, svc.ensureJWTSecret(ctx))
	require.Equal(t, first, svc.config.Auth.JWTSecret)

	// The env var short-circuits everything.
	t.Setenv("SP_AUTH_JWT_SECRET", "from-env")

	svc.config.Auth.JWTSecret = ""
	require.NoError(t, svc.ensureJWTSecret(ctx))
	require.Empty(t, svc.config.Auth.JWTSecret)
}

// TestEnsureJWTSecretKnownPlaceholders covers the dev-compose trap: the
// repository's development compose ships a publicly-known JWT secret, and
// neither the config value nor the env var may keep it in place as the signing
// secret. A real env secret still short-circuits (TestEnsureJWTSecret).
func TestEnsureJWTSecretKnownPlaceholders(t *testing.T) {
	svc, ctx, _ := newCoverService(t)

	// Seed a real secret in the database via a first generation.
	require.NoError(t, svc.ensureJWTSecret(ctx))
	stored := svc.config.Auth.JWTSecret
	require.NotEmpty(t, stored)

	// The dev compose constant in config is a placeholder: reload from the DB.
	svc.config.Auth.JWTSecret = config.DevJWTSecret
	require.NoError(t, svc.ensureJWTSecret(ctx))
	require.Equal(t, stored, svc.config.Auth.JWTSecret)

	// A placeholder env var does NOT short-circuit: the stored secret wins.
	t.Setenv("SP_AUTH_JWT_SECRET", config.DevJWTSecret)
	svc.config.Auth.JWTSecret = ""
	require.NoError(t, svc.ensureJWTSecret(ctx))
	require.Equal(t, stored, svc.config.Auth.JWTSecret)

	// Same for the historical default placeholder in the env.
	t.Setenv("SP_AUTH_JWT_SECRET", config.DefaultJWTSecret)
	svc.config.Auth.JWTSecret = config.DefaultJWTSecret
	require.NoError(t, svc.ensureJWTSecret(ctx))
	require.Equal(t, stored, svc.config.Auth.JWTSecret)

	// With no stored secret and a placeholder env, a real one is generated.
	svc2, ctx2, _ := newCoverService(t)
	t.Setenv("SP_AUTH_JWT_SECRET", config.DevJWTSecret)
	require.NoError(t, svc2.ensureJWTSecret(ctx2))
	require.False(t, config.IsPlaceholderJWTSecret(svc2.config.Auth.JWTSecret))
	require.NotEmpty(t, svc2.config.Auth.JWTSecret)
}

// TestEnsureJWTSecretDocsPlaceholder covers the docs trap: the configuration
// docs once published "your-secure-random-secret" as a copy-paste example, so
// an env var carrying it must not become the signing key.
func TestEnsureJWTSecretDocsPlaceholder(t *testing.T) {
	svc, ctx, _ := newCoverService(t)

	t.Setenv("SP_AUTH_JWT_SECRET", "your-secure-random-secret")
	svc.config.Auth.JWTSecret = "your-secure-random-secret"
	require.NoError(t, svc.ensureJWTSecret(ctx))

	generated := svc.config.Auth.JWTSecret
	require.NotEqual(t, "your-secure-random-secret", generated)
	require.False(t, config.IsPlaceholderJWTSecret(generated))

	param, err := svc.db.GetSystemParameter(ctx, string(KeyJWTSecret))
	require.NoError(t, err)
	require.NotNil(t, param)
	require.Equal(t, generated, param.Value["value"])
}

// TestEnsureJWTSecretShortWarns checks a short real secret is kept (refusing
// it would lock the operator out) but logged as a warning.
func TestEnsureJWTSecretShortWarns(t *testing.T) {
	var buf bytes.Buffer

	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	svc, ctx, _ := newCoverService(t)

	t.Setenv("SP_AUTH_JWT_SECRET", "short-real-secret")
	require.NoError(t, svc.ensureJWTSecret(ctx))
	require.Contains(t, buf.String(), "shorter than the recommended minimum")
	require.Contains(t, buf.String(), "source=SP_AUTH_JWT_SECRET")

	buf.Reset()
	t.Setenv("SP_AUTH_JWT_SECRET", "")
	svc.config.Auth.JWTSecret = "also-short"
	require.NoError(t, svc.ensureJWTSecret(ctx))
	require.Equal(t, "also-short", svc.config.Auth.JWTSecret)
	require.Contains(t, buf.String(), "source=auth.jwt_secret")

	buf.Reset()
	svc.config.Auth.JWTSecret = strings.Repeat("k", config.MinJWTSecretLength)
	require.NoError(t, svc.ensureJWTSecret(ctx))
	require.NotContains(t, buf.String(), "shorter than the recommended minimum")
}
