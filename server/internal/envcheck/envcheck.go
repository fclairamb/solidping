// Package envcheck warns about SP_*-prefixed environment variables that no
// recognized configuration key binds, so a typo'd setting is surfaced at
// startup instead of silently becoming a no-op (the
// SP_RATE_LIMITING_TRUSTED_PROXIES incident, which should have been
// SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES).
//
// It is WARN-only and never fails startup: unknown SP_* names are legitimate in
// mixed fleets running newer and older binaries side by side, and operators may
// export arbitrary SP_* vars that a check references through the ${env:...}
// config-as-code mechanism — names unknowable at startup. The registry follows
// a strict "recognized = actually binds" rule, so a documented-but-unbound
// variable is intentionally reported.
//
// SP_ENCRYPTION_MASTER_KEY used to be the standing example here. It was not a
// harmless one: the credentials service kept storing secrets in plaintext while
// its own remediation message named that variable, so the warning was correct
// and the advice it contradicted was not. It binds since applyEncryptionEnv.
package envcheck

import (
	"context"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

// maxSuggestDistance is the Levenshtein ceiling for the character-typo rule of
// the did-you-mean matcher. A missing/extra whole segment (distance 7+ for
// "SERVER_") is handled by the segment-subsequence rule instead.
const maxSuggestDistance = 3

// Warning describes one unrecognized SP_* variable and, when the matcher found
// a close recognized name, a did-you-mean suggestion (empty when none matched).
type Warning struct {
	Name       string
	Suggestion string
}

// WarnUnrecognizedEnv scans the process environment and logs one WARN line per
// unrecognized SP_* variable through the package-level slog default (already
// configured by the caller). It never returns an error and never fails startup.
func WarnUnrecognizedEnv(ctx context.Context) {
	warnings := Check()
	for i := range warnings {
		warning := &warnings[i]
		if warning.Suggestion != "" {
			slog.WarnContext(ctx,
				"Unrecognized SP_* environment variable is ignored; did you mean another name?",
				"name", warning.Name, "didYouMean", warning.Suggestion)
		} else {
			slog.WarnContext(ctx,
				"Unrecognized SP_* environment variable is ignored",
				"name", warning.Name)
		}
	}
}

// Check scans the current process environment for SP_*-prefixed variables that
// no recognized configuration key binds and returns one Warning per unrecognized
// name (with a did-you-mean suggestion when the matcher found a close one). It
// only reads os.Environ() and mutates nothing. The result is sorted by name for
// deterministic logging.
func Check() []Warning {
	return checkEnviron(os.Environ())
}

// checkEnviron is the testable core of Check: it takes an explicit environment
// (as returned by os.Environ, "NAME=value" entries) so tests need not mutate the
// real process environment.
func checkEnviron(environ []string) []Warning {
	recognized := recognizedEnvVars()

	// Deterministic, sorted list of known names for stable suggestions.
	known := make([]string, 0, len(recognized))
	for name := range recognized {
		known = append(known, name)
	}
	sort.Strings(known)

	var warnings []Warning
	seen := make(map[string]struct{})
	for _, entry := range environ {
		name := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			name = entry[:i]
		}
		if !strings.HasPrefix(name, "SP_") {
			continue
		}
		if _, ok := recognized[name]; ok {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}

		warnings = append(warnings, Warning{
			Name:       name,
			Suggestion: suggest(name, known),
		})
	}

	sort.Slice(warnings, func(i, j int) bool { return warnings[i].Name < warnings[j].Name })

	return warnings
}

// recognizedEnvVars unions every source that makes an SP_* name actually bind:
//
//  1. koanf-reachable struct paths and 2. the manual readers in config.Load,
//     both via config.RecognizedEnvVars.
//  3. the systemconfig parameter table, via systemconfig.KnownEnvVars.
//  4. the ad-hoc SP_PERFORMANCE_* resolvers in internal/jobs/jobtypes.
//
// plus SP_* names read directly outside config.Load and an exact-name allowlist
// of CLI vars an operator may have exported in the same shell.
func recognizedEnvVars() map[string]struct{} {
	set := make(map[string]struct{})
	add := func(names []string) {
		for _, name := range names {
			set[name] = struct{}{}
		}
	}

	add(config.RecognizedEnvVars())  // sources 1 + 2
	add(systemconfig.KnownEnvVars()) // source 3
	add(performanceEnvVars())        // source 4
	add(otherManualReaderEnvVars())
	add(cliAllowlist())

	return set
}

// performanceEnvVars enumerates the ad-hoc SP_PERFORMANCE_* resolvers in
// internal/jobs/jobtypes (job_aggregation.go, job_jobs_cleanup.go) — a second
// env→parameter chain that never reaches the systemconfig table. Listed here to
// avoid importing the heavy jobtypes package for four constants.
func performanceEnvVars() []string {
	return []string{
		"SP_PERFORMANCE_AGGREGATION_RETENTION_RAW_HOURS",
		"SP_PERFORMANCE_AGGREGATION_RETENTION_HOUR_DAYS",
		"SP_PERFORMANCE_AGGREGATION_RETENTION_DAY_MONTHS",
		"SP_PERFORMANCE_JOBS_SOFT_DELETE_HOURS",
		"SP_PERFORMANCE_JOBS_HARD_DELETE_HOURS",
	}
}

// otherManualReaderEnvVars are SP_* names read directly via os.Getenv outside
// config.Load: the SaaS entitlements wiring (internal/app/saas.go), the region
// seeder (internal/app/regions_seed.go), the duplicate base-URL read in the
// startup job (internal/jobs/jobtypes/job_startup.go — same meaning as the
// systemconfig SP_BASE_URL, both live), and the API client's HTTP-call logging
// toggle (pkg/client/client.go). Listed here to avoid heavy or cyclic imports.
func otherManualReaderEnvVars() []string {
	return []string{
		"SP_ENTITLEMENTS_SERVICE_TOKEN",
		"SP_ENTITLEMENTS_UPGRADE_URL_TEMPLATE",
		"SP_ENTITLEMENTS_ADMIN_WRITES_ENABLED",
		"SP_ENTITLEMENTS_BILLING_INBOUND_SECRET",
		"SP_ENTITLEMENTS_SERVICE_SIGNING_KEYS",
		"SP_ENTITLEMENTS_OUTBOUND_SIGNING_KEYS",
		"SP_ENTITLEMENTS_ALLOW_LEGACY_SERVICE_TOKEN",
		"SP_REGIONS",
		"SP_SERVER_BASE_URL",
		"SP_LOG_HTTP_CALLS",
	}
}

// cliAllowlist are SP_* names that configure the CLI / scenario driver rather
// than the server, but an operator may legitimately have exported them in the
// same shell that starts the server, so they must not warn. Skipped by exact
// name. SP_TOKEN: pkg/cli/commands.go; SP_SERVER, SP_ORG:
// cmd/scenariodriver/main.go.
func cliAllowlist() []string {
	return []string{"SP_TOKEN", "SP_SERVER", "SP_ORG"}
}
