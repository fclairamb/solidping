package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEnvNameForKoanfPath pins the reverse mapping from a koanf leaf path to the
// SP_* env var that reaches it, including the snake_case exclusion that is the
// crux of the "recognized = actually binds" rule.
func TestEnvNameForKoanfPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path     string
		wantName string
		wantOK   bool
	}{
		{"otel.endpoint", "SP_OTEL_ENDPOINT", true},
		{"db.type", "SP_DB_TYPE", true},
		{"server.listen", "SP_SERVER_LISTEN", true},
		{"runmode", "SP_RUNMODE", true},
		{"app.github.repo", "SP_APP_GITHUB_REPO", true},
		// Any snake_case segment makes the path unreachable via env.
		{"encryption.master_key", "", false},
		{"server.rate_limiting.trusted_proxies", "", false},
		{"auth.jwt_secret", "", false},
		{"aggregation.retention_raw", "", false},
	}

	for _, tc := range cases {
		r := require.New(t)
		name, ok := envNameForKoanfPath(tc.path)
		r.Equalf(tc.wantOK, ok, "ok for %q", tc.path)
		r.Equalf(tc.wantName, name, "name for %q", tc.path)
	}
}

// TestRecognizedEnvVars asserts the union of koanf-reflected paths and manual
// readers recognizes names that actually bind and, critically, does NOT
// recognize documented-but-unbound names — the whole point of the check.
func TestRecognizedEnvVars(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	set := make(map[string]struct{})
	for _, name := range RecognizedEnvVars() {
		set[name] = struct{}{}
	}

	// Source 1 (koanf-reflected) exemplars.
	r.Contains(set, "SP_OTEL_ENDPOINT")
	r.Contains(set, "SP_DB_TYPE")
	r.Contains(set, "SP_PROMETHEUS_ENABLED")
	// Source 2 (manual reader) exemplars — koanf can't reach these snake_case
	// paths, so their presence proves the manual-reader list is unioned in.
	r.Contains(set, "SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES")
	r.Contains(set, "SP_AUTH_PASSWORD_ARGON2_KEY_LENGTH")
	r.Contains(set, "SP_LOG_LEVEL")

	// Confirmed-broken bindings must NOT be recognized (see spec open question):
	// SP_ENCRYPTION_MASTER_KEY transforms to encryption.master.key but the tag
	// is master_key, so it never binds. Adding it here would paper over the bug.
	r.NotContains(set, "SP_ENCRYPTION_MASTER_KEY")
	r.NotContains(set, "SP_ENCRYPTION_MASTER_KEY_FILE")
	r.NotContains(set, "SP_ENCRYPTION_AUTO_MIGRATE")
	// The k8xp typo must not be recognized (it is the near-miss the check warns on).
	r.NotContains(set, "SP_RATE_LIMITING_TRUSTED_PROXIES")
}

// TestManualReaderEnvVarsBind spot-checks that a representative sample of the
// manual-reader names actually bind through config.Load — guarding against a
// name drifting in the list without a corresponding reader (which would produce
// false negatives: a typo of a "recognized" name that nothing reads).
func TestManualReaderEnvVarsBind(t *testing.T) {
	t.Setenv("SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES", "7")
	t.Setenv("SP_DOCS_HOST", "docs.example.test")
	t.Setenv("SP_JOBS_REAPER_INTERVAL", "3m")
	t.Setenv("SP_REALTIME_MAX_CONNECTIONS", "1234")
	t.Setenv("SP_ENTITLEMENTS_SMS_RUNAWAY_PER_HOUR", "42")
	t.Setenv("SP_ENTITLEMENTS_CALL_RUNAWAY_PER_HOUR", "7")

	r := require.New(t)
	cfg, err := Load()
	r.NoError(err)
	r.Equal(7, cfg.Server.RateLimiting.TrustedProxies)
	r.Equal("docs.example.test", cfg.Server.DocsHost)
	r.Equal(3*60, int(cfg.Jobs.ReaperInterval.Seconds()))
	r.Equal(1234, cfg.Realtime.MaxConnections)
	r.Equal(42, cfg.Entitlements.SMSRunawayPerHour)
	r.Equal(7, cfg.Entitlements.CallRunawayPerHour)
}
