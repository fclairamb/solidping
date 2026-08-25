package envcheck

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCheckEnvironRecognizedNameNoWarning: a recognized name produces no warning.
func TestCheckEnvironRecognizedNameNoWarning(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	warnings := checkEnviron([]string{"SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES=5"})
	r.Empty(warnings)
}

// TestCheckEnvironUnknownNameWarns: an unknown name produces exactly one warning.
func TestCheckEnvironUnknownNameWarns(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	warnings := checkEnviron([]string{"SP_TOTALLY_MADE_UP=1"})
	r.Len(warnings, 1)
	r.Equal("SP_TOTALLY_MADE_UP", warnings[0].Name)
	r.Empty(warnings[0].Suggestion)
}

// TestCheckEnvironNearMissSuggests is the regression test for the near-miss incident:
// the near-miss warns AND suggests the real name (asserting the suggestion, not
// merely that a warning fired).
func TestCheckEnvironNearMissSuggests(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	warnings := checkEnviron([]string{"SP_RATE_LIMITING_TRUSTED_PROXIES=5"})
	r.Len(warnings, 1)
	r.Equal("SP_RATE_LIMITING_TRUSTED_PROXIES", warnings[0].Name)
	r.Equal("SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES", warnings[0].Suggestion)
}

// TestCheckEnvironIgnoresNonSPAndAllowlist: non-SP_ vars and the CLI allowlist
// never warn.
func TestCheckEnvironIgnoresNonSPAndAllowlist(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	warnings := checkEnviron([]string{
		"PATH=/usr/bin",
		"GITHUB_ISSUES_TOKEN=abc",
		"SP_TOKEN=secret",
		"SP_SERVER=https://example.test",
		"SP_ORG=acme",
	})
	r.Empty(warnings)
}

// TestCheckEnvironEncryptionMasterKeyWarns is the required acceptance criterion:
// SP_ENCRYPTION_MASTER_KEY never binds (confirmed broken), so under the
// "recognized = actually binds" rule the check must flag it when set. Silencing
// this by adding the name to the registry would paper over the underlying bug.
func TestCheckEnvironEncryptionMasterKeyWarns(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	warnings := checkEnviron([]string{"SP_ENCRYPTION_MASTER_KEY=deadbeef"})
	r.Len(warnings, 1)
	r.Equal("SP_ENCRYPTION_MASTER_KEY", warnings[0].Name)
}

// TestCheckEnvironDedupes: a name appearing twice yields a single warning.
func TestCheckEnvironDedupes(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	warnings := checkEnviron([]string{"SP_MADE_UP=a", "SP_MADE_UP=b"})
	r.Len(warnings, 1)
}

// TestCheckEnvironCleanEnvironmentNoWarnings is the negative control: an
// environment carrying only recognized names (one per source) plus non-SP noise
// produces zero warnings.
func TestCheckEnvironCleanEnvironmentNoWarnings(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	warnings := checkEnviron([]string{
		"PATH=/usr/bin",
		"SP_OTEL_ENDPOINT=http://collector:4317",    // source 1
		"SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES=3", // source 2
		"SP_AUTH_JWT_SECRET=shh",                    // source 3
		"SP_PERFORMANCE_JOBS_SOFT_DELETE_HOURS=48",  // source 4
		"SP_BASE_URL=https://a.test",                // systemconfig base url
		"SP_SERVER_BASE_URL=https://a.test",         // startup-job base url
		"SP_TOKEN=cli",                              // allowlist
	})
	r.Empty(warnings)
}

// TestRecognizedSetCoversAllFourSources is the positive control against the whole
// registry: it must contain a name from EACH of the four sources, so a registry
// that silently loses a source is caught here rather than spraying false
// positives in production.
func TestRecognizedSetCoversAllFourSources(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	set := recognizedEnvVars()

	r.Contains(set, "SP_OTEL_ENDPOINT", "source 1 (koanf-reflected) missing")
	r.Contains(set, "SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES", "source 2 (manual reader) missing")
	r.Contains(set, "SP_AUTH_JWT_SECRET", "source 3 (systemconfig) missing")
	r.Contains(set, "SP_PERFORMANCE_JOBS_SOFT_DELETE_HOURS", "source 4 (SP_PERFORMANCE_*) missing")

	// Spec open question 2: both base-URL spellings are live and must be recognized.
	r.Contains(set, "SP_BASE_URL")
	r.Contains(set, "SP_SERVER_BASE_URL")
}

// TestSuggestAgainstFullRegistry exercises the matcher against the real,
// complete registry (not a hand-picked slice) for the two headline cases.
func TestSuggestAgainstFullRegistry(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	set := recognizedEnvVars()
	known := make([]string, 0, len(set))
	for name := range set {
		known = append(known, name)
	}
	// suggest requires a sorted slice for deterministic tie-breaks.
	sort.Strings(known)

	r.Equal("SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES",
		suggest("SP_RATE_LIMITING_TRUSTED_PROXIES", known))
	r.Equal("SP_LOG_LEVEL", suggest("SP_LOG_LEVL", known))
}
