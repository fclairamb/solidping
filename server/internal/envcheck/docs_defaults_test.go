package envcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/defaults"
	"github.com/fclairamb/solidping/server/internal/entitlements"
)

// docRow is one `| \`SP_X\` | ... |` row of a markdown table whose header has a
// Default column.
type docRow struct {
	file    string
	line    int
	name    string
	literal string // the default when the cell is exactly one backticked token
	hasLit  bool
}

var (
	docNameRe    = regexp.MustCompile("^`(SP_[A-Z0-9_]+)`")
	docLiteralRe = regexp.MustCompile("^`([^`]+)`$")
)

// splitRow splits a markdown table row into trimmed cells.
func splitRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")

	cells := strings.Split(line, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}

	return cells
}

// parseDocRows extracts the documented env-var rows of every table with a
// Default column.
func parseDocRows(file, content string) []docRow {
	var rows []docRow

	defaultCol := -1

	for i, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			defaultCol = -1

			continue
		}

		cells := splitRow(trimmed)

		if defaultCol < 0 {
			for c, cell := range cells {
				if strings.EqualFold(cell, "Default") {
					defaultCol = c
				}
			}

			continue
		}

		m := docNameRe.FindStringSubmatch(cells[0])
		if m == nil || defaultCol >= len(cells) {
			continue
		}

		row := docRow{file: file, line: i + 1, name: m[1]}
		if lit := docLiteralRe.FindStringSubmatch(cells[defaultCol]); lit != nil {
			row.literal, row.hasLit = lit[1], true
		}

		rows = append(rows, row)
	}

	return rows
}

// sameValue compares a documented and a code default after normalizing
// durations (5m == 5m0s), bools and numbers.
func sameValue(doc, code string) bool {
	if doc == code {
		return true
	}

	if a, err := time.ParseDuration(doc); err == nil {
		if b, err2 := time.ParseDuration(code); err2 == nil {
			return a == b
		}
	}

	if a, err := strconv.ParseBool(doc); err == nil {
		if b, err2 := strconv.ParseBool(code); err2 == nil {
			return a == b
		}
	}

	if a, err := strconv.ParseFloat(doc, 64); err == nil {
		if b, err2 := strconv.ParseFloat(code, 64); err2 == nil {
			return a == b
		}
	}

	return false
}

// checkDocRows returns one problem per drifting row.
func checkDocRows(
	rows []docRow, recognized map[string]struct{}, koanfDefaults map[string]string,
	accessors map[string]accessor, cfg *config.Config,
) []string {
	var problems []string

	for _, row := range rows {
		where := fmt.Sprintf("%s:%d", row.file, row.line)

		if _, ok := recognized[row.name]; !ok {
			problems = append(problems, fmt.Sprintf("%s: %s is documented but nothing binds it", where, row.name))

			continue
		}

		if !row.hasLit {
			continue
		}

		var code string

		if acc, ok := accessors[row.name]; ok {
			code = acc(cfg)
		} else if v, ok := koanfDefaults[row.name]; ok {
			code = v
		} else {
			problems = append(problems, fmt.Sprintf(
				"%s: add an accessor for %s in docs_defaults_test.go (documented default %q)", where, row.name, row.literal))

			continue
		}

		if !sameValue(row.literal, code) {
			problems = append(problems, fmt.Sprintf(
				"%s: %s documents default %q but the code default is %q", where, row.name, row.literal, code))
		}
	}

	return problems
}

type conf = config.Config

type accessor = func(*config.Config) string

func intOf(get func(*config.Config) int) accessor {
	return func(c *config.Config) string { return strconv.Itoa(get(c)) }
}

func boolOf(get func(*config.Config) bool) accessor {
	return func(c *config.Config) string { return strconv.FormatBool(get(c)) }
}

func durOf(get func(*config.Config) time.Duration) accessor {
	return func(c *config.Config) string { return get(c).String() }
}

func strOf(get func(*config.Config) string) accessor { return get }

func fixed(value string) accessor { return func(*config.Config) string { return value } }

// defaultAccessors reads, for names config.Load binds by hand or resolves
// later, the effective default. Derived defaults use the resolved value.
//
//nolint:gochecknoglobals // test lookup table
var defaultAccessors = map[string]accessor{
	"SP_SERVER_CHECK_WORKER_NB": intOf(func(c *conf) int { return c.Server.CheckWorker.Nb }),
	"SP_SERVER_JOB_WORKER_NB":   intOf(func(c *conf) int { return c.Server.JobWorker.Nb }),
	"SP_SERVER_CHECK_WORKER_FETCH_MAX_AHEAD": durOf(func(c *conf) time.Duration {
		return c.Server.CheckWorker.FetchMaxAhead
	}),
	"SP_ADMIN_EMAIL":            fixed(defaults.Email),
	"SP_FILESTORAGE_LOCAL_ROOT": strOf(func(c *conf) string { return c.FileStorage.LocalRoot }),
	"SP_LOG_LEVEL":              strOf(func(c *conf) string { return strings.ToLower(c.LogLevel.String()) }),
	"SP_LOG_FORMAT":             strOf(func(c *conf) string { return string(c.LogFormat) }),
	"SP_DEMO_ORG_SLUG":          strOf(func(c *conf) string { return c.Demo.ResolvedOrgSlug() }),
	"SP_DEMO_CHECK_TTL":         durOf(func(c *conf) time.Duration { return c.Demo.ResolvedCheckTTL() }),
	"SP_DEMO_CLEANUP_INTERVAL": durOf(func(c *conf) time.Duration {
		return c.Demo.ResolvedCleanupInterval()
	}),
	"SP_BASE_URL":                  strOf(func(c *conf) string { return c.Server.BaseURL }),
	"SP_ENCRYPTION_AUTO_MIGRATE":   boolOf(func(c *conf) bool { return c.Encryption.AutoMigrate }),
	"SP_POSTHOG_HOST":              strOf(func(c *conf) string { return c.PostHog.ResolvedHost() }),
	"SP_RUNTIME_AUTO_MEMORY_LIMIT": boolOf(func(c *conf) bool { return c.Runtime.AutoMemoryLimit }),
	"SP_RUNTIME_MEMORY_LIMIT_RATIO": func(c *conf) string {
		return strconv.FormatFloat(c.Runtime.MemoryLimitRatio, 'f', -1, 64)
	},
	"SP_AI_MAX_TURNS":             intOf(func(c *conf) int { return c.AI.MaxTurns }),
	"SP_SHUTDOWN_TIMEOUT":         durOf(func(c *conf) time.Duration { return c.Server.ShutdownTimeout }),
	"SP_EXIT_WITH_PARENT":         boolOf(func(c *conf) bool { return c.Server.ExitWithParent }),
	"SP_CUSTOM_DOMAIN_CNAME_MODE": strOf(func(c *conf) string { return string(c.CustomDomainCNAMEMode()) }),
	"SP_ACME_LISTEN_HTTP":         strOf(func(c *conf) string { return c.ACME.ListenHTTP }),
	"SP_ACME_LISTEN_HTTPS":        strOf(func(c *conf) string { return c.ACME.ListenHTTPS }),
	"SP_ACME_FALLBACK_UPSTREAM_PROXY_PROTOCOL": boolOf(func(c *conf) bool {
		return c.ACME.FallbackUpstreamProxyProtocol
	}),
	"SP_ACME_PROXY_PROTOCOL":                    boolOf(func(c *conf) bool { return c.ACME.ProxyProtocol }),
	"SP_AUTH_IMPERSONATION_ENABLED":             boolOf(func(c *conf) bool { return c.Auth.ImpersonationEnabled }),
	"SP_SMS_PROVIDER":                           strOf(func(c *conf) string { return c.SMS.ResolvedProvider() }),
	"SP_SMS_GLOBAL_RUNAWAY_PER_HOUR":            intOf(func(c *conf) int { return c.SMS.ResolvedGlobalRunawayPerHour() }),
	"SP_ENTITLEMENTS_TELEGRAM_RUNAWAY_PER_HOUR": fixed(strconv.Itoa(entitlements.DefaultTelegramRunawayPerHour)),
	"SP_ENTITLEMENTS_WHATSAPP_RUNAWAY_PER_HOUR": fixed(strconv.Itoa(entitlements.DefaultWhatsAppRunawayPerHour)),
	"SP_NODE_REGION":                            strOf(func(c *conf) string { return c.Server.CheckWorker.Region }),
}

func docsRepoRoot(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)

	// <root>/server/internal/envcheck/docs_defaults_test.go
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
}

//nolint:paralleltest // unsets process-wide SP_* variables
func TestDocumentedDefaultsMatchCode(t *testing.T) {
	root := docsRepoRoot(t)

	files, err := filepath.Glob(filepath.Join(root, "web", "docs", "docs", "configuration", "*.md"))
	require.NoError(t, err)

	files = append(files, filepath.Join(root, "wiki", "conventions", "runners.md"))

	rows := make([]docRow, 0, len(files)*16)

	for _, f := range files {
		content, readErr := os.ReadFile(f)
		require.NoError(t, readErr)

		rel, _ := filepath.Rel(root, f)
		rows = append(rows, parseDocRows(rel, string(content))...)
	}

	require.NotEmpty(t, rows)

	// Defaults are read with no SP_* variable set.
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "SP_") {
			t.Setenv(name, "")
			require.NoError(t, os.Unsetenv(name))
		}
	}

	cfg, err := config.Load()
	require.NoError(t, err)

	problems := checkDocRows(rows, recognizedEnvVars(), config.DefaultsByEnvVar(cfg), defaultAccessors, cfg)
	sort.Strings(problems)
	require.Empty(t, problems, "documented env vars drifted from the code:\n%s", strings.Join(problems, "\n"))
}

func TestDocDefaultsCheckerPositiveControls(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.Server.CheckWorker.Nb = 25

	table := "| Variable | Default | Description |\n|---|---|---|\n" +
		"| `SP_SERVER_CHECK_WORKER_NB` | `3` | x |\n" +
		"| `SP_SERVER_JOB_WORKER_FETCH_MAX_AHEAD` | `5m` | x |\n" +
		"| `SP_FAKE_BOUND` | `7` | x |\n" +
		"| `SP_SERVER_JOB_WORKER_NB` | derived | x |\n"
	rows := parseDocRows("fixture.md", table)
	require.Len(t, rows, 4)
	require.Equal(t, 3, rows[0].line)

	recognized := map[string]struct{}{
		"SP_SERVER_CHECK_WORKER_NB": {}, "SP_FAKE_BOUND": {}, "SP_SERVER_JOB_WORKER_NB": {},
	}
	problems := checkDocRows(rows, recognized, nil, defaultAccessors, cfg)
	require.Len(t, problems, 3)
	require.Contains(t, problems[0], `"3"`)
	require.Contains(t, problems[0], `"25"`)
	require.Contains(t, problems[1], "SP_SERVER_JOB_WORKER_FETCH_MAX_AHEAD is documented but nothing binds it")
	require.Contains(t, problems[2], "add an accessor for SP_FAKE_BOUND")
}

func TestSameValueNormalization(t *testing.T) {
	t.Parallel()

	require.True(t, sameValue("5m", "5m0s"))
	require.False(t, sameValue("30s", "15s"))
	require.False(t, sameValue("true", "false"))
	require.True(t, sameValue("25", "25"))
	require.True(t, sameValue("0.9", "0.90"))
}
