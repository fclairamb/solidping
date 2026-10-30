package envcheck

import (
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

// docRow is one documented setting: a table row whose first cell is a
// backticked SP_ name, with the raw text of its Default cell.
type docRow struct {
	file string
	line int
	name string
	def  string
}

var (
	firstCellRe = regexp.MustCompile("^`(SP_[A-Z0-9_]+)`")
	literalRe   = regexp.MustCompile("^`([^`]+)`$")
)

// parseDocTables extracts the rows of every markdown table whose header has a
// "Default" column and whose first cell starts with a backticked SP_ name.
func parseDocTables(file, content string) []docRow {
	var (
		rows       []docRow
		defaultCol = -1
	)

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

		if len(cells) <= defaultCol {
			continue
		}

		if m := firstCellRe.FindStringSubmatch(cells[0]); m != nil {
			rows = append(rows, docRow{file: file, line: i + 1, name: m[1], def: cells[defaultCol]})
		}
	}

	return rows
}

func splitRow(line string) []string {
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")

	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}

	return parts
}

// docsDefaultAccessors gives the code value of a documented default that the
// koanf struct tags cannot provide: manual-reader settings and defaults
// resolved after Load.
//
//nolint:gochecknoglobals // test lookup table
var docsDefaultAccessors = map[string]func(*config.Config) string{
	"SP_SERVER_CHECK_WORKER_NB": intOf(func(c *config.Config) int { return c.Server.CheckWorker.Nb }),
	"SP_SERVER_JOB_WORKER_NB":   intOf(func(c *config.Config) int { return c.Server.JobWorker.Nb }),
	"SP_SERVER_CHECK_WORKER_FETCH_MAX_AHEAD": durOf(func(c *config.Config) time.Duration {
		return c.Server.CheckWorker.FetchMaxAhead
	}),
	"SP_ADMIN_EMAIL":            fixed(defaults.Email),
	"SP_FILESTORAGE_LOCAL_ROOT": func(c *config.Config) string { return c.FileStorage.LocalRoot },
	"SP_LOG_LEVEL": func(c *config.Config) string {
		return strings.ToLower(c.LogLevel.String())
	},
	"SP_LOG_FORMAT":     func(c *config.Config) string { return string(c.LogFormat) },
	"SP_DEMO_ORG_SLUG":  func(c *config.Config) string { return c.Demo.OrgSlug },
	"SP_DEMO_CHECK_TTL": durOf(func(c *config.Config) time.Duration { return c.Demo.CheckTTL }),
	"SP_DEMO_CLEANUP_INTERVAL": durOf(func(c *config.Config) time.Duration {
		return c.Demo.CleanupInterval
	}),
	"SP_BASE_URL":                  func(c *config.Config) string { return c.Server.BaseURL },
	"SP_ENCRYPTION_AUTO_MIGRATE":   boolOf(func(c *config.Config) bool { return c.Encryption.AutoMigrate }),
	"SP_POSTHOG_HOST":              fixed(config.DefaultPostHogHost),
	"SP_RUNTIME_AUTO_MEMORY_LIMIT": boolOf(func(c *config.Config) bool { return c.Runtime.AutoMemoryLimit }),
	"SP_RUNTIME_MEMORY_LIMIT_RATIO": func(c *config.Config) string {
		return strconv.FormatFloat(c.Runtime.MemoryLimitRatio, 'f', -1, 64)
	},
	"SP_AI_MAX_TURNS":     intOf(func(c *config.Config) int { return c.AI.MaxTurns }),
	"SP_SHUTDOWN_TIMEOUT": durOf(func(c *config.Config) time.Duration { return c.Server.ShutdownTimeout }),
	"SP_EXIT_WITH_PARENT": boolOf(func(c *config.Config) bool { return c.Server.ExitWithParent }),
	"SP_CUSTOM_DOMAIN_CNAME_MODE": func(c *config.Config) string {
		return c.Server.CustomDomainCNAMEMode
	},
	"SP_ACME_LISTEN_HTTP":  func(c *config.Config) string { return c.ACME.ListenHTTP },
	"SP_ACME_LISTEN_HTTPS": func(c *config.Config) string { return c.ACME.ListenHTTPS },
	"SP_ACME_FALLBACK_UPSTREAM_PROXY_PROTOCOL": boolOf(func(c *config.Config) bool {
		return c.ACME.FallbackUpstreamProxyProtocol
	}),
	"SP_ACME_PROXY_PROTOCOL": boolOf(func(c *config.Config) bool { return c.ACME.ProxyProtocol }),
	"SP_AUTH_IMPERSONATION_ENABLED": boolOf(func(c *config.Config) bool {
		return c.Auth.ImpersonationEnabled
	}),
	"SP_SMS_PROVIDER": func(c *config.Config) string { return c.SMS.ResolvedProvider() },
	"SP_SMS_GLOBAL_RUNAWAY_PER_HOUR": intOf(func(c *config.Config) int {
		return c.SMS.ResolvedGlobalRunawayPerHour()
	}),
	"SP_ENTITLEMENTS_TELEGRAM_RUNAWAY_PER_HOUR": fixed(strconv.Itoa(entitlements.DefaultTelegramRunawayPerHour)),
	"SP_ENTITLEMENTS_WHATSAPP_RUNAWAY_PER_HOUR": fixed(strconv.Itoa(entitlements.DefaultWhatsAppRunawayPerHour)),
	"SP_NODE_REGION":               func(c *config.Config) string { return c.Server.CheckWorker.Region },
	"SP_SLACK_SOCKET_MODE_ENABLED": boolOf(func(c *config.Config) bool { return c.Slack.SocketModeEnabled }),
	"SP_K8S_ALLOW_IN_CLUSTER":      boolOf(func(c *config.Config) bool { return c.Kubernetes.AllowInCluster }),
	"SP_LDAP_START_TLS":            boolOf(func(c *config.Config) bool { return c.LDAP.StartTLS }),
	"SP_LDAP_INSECURE_SKIP_VERIFY": boolOf(func(c *config.Config) bool { return c.LDAP.InsecureSkipVerify }),
	// Blank in the struct, resolved at sign-in time.
	"SP_LDAP_USER_FILTER":     fixed(config.LDAPDefaultUserFilter),
	"SP_LDAP_EMAIL_ATTRIBUTE": fixed(config.LDAPDefaultEmailAttribute),
	"SP_LDAP_NAME_ATTRIBUTE":  fixed(config.LDAPDefaultNameAttribute),

	"SP_AUTH_FORM_GUARD_ENABLED": boolOf(func(c *config.Config) bool { return c.Auth.FormGuard.Enabled }),
	"SP_AUTH_FORM_GUARD_MIN_FILL_TIME": durOf(func(c *config.Config) time.Duration {
		return c.Auth.FormGuard.MinFillTime
	}),
	"SP_AUTH_FORM_GUARD_TICKET_TTL": durOf(func(c *config.Config) time.Duration {
		return c.Auth.FormGuard.TicketTTL
	}),
	"SP_AUTH_FORM_GUARD_PER_IP_PER_HOUR": intOf(func(c *config.Config) int {
		return c.Auth.FormGuard.PerIPPerHour
	}),
	"SP_AUTH_FORM_GUARD_PER_EMAIL_PER_HOUR": intOf(func(c *config.Config) int {
		return c.Auth.FormGuard.PerEmailPerHour
	}),
	"SP_STATUS_SUBSCRIBERS_CONFIRM_PER_RECIPIENT_PER_HOUR": intOf(func(c *config.Config) int {
		return c.StatusSubscribers.ConfirmPerRecipientPerHour
	}),
	"SP_STATUS_SUBSCRIBERS_CONFIRM_PER_PAGE_PER_HOUR": intOf(func(c *config.Config) int {
		return c.StatusSubscribers.ConfirmPerPagePerHour
	}),
	"SP_STATUS_SUBSCRIBERS_CONFIRM_PER_IP_PER_HOUR": intOf(func(c *config.Config) int {
		return c.StatusSubscribers.ConfirmPerIPPerHour
	}),
}

func fixed(v string) func(*config.Config) string { return func(*config.Config) string { return v } }

func intOf(f func(*config.Config) int) func(*config.Config) string {
	return func(c *config.Config) string { return strconv.Itoa(f(c)) }
}

func boolOf(f func(*config.Config) bool) func(*config.Config) string {
	return func(c *config.Config) string { return strconv.FormatBool(f(c)) }
}

func durOf(f func(*config.Config) time.Duration) func(*config.Config) string {
	return func(c *config.Config) string { return f(c).String() }
}

func sameValue(doc, code string) bool {
	if doc == code {
		return true
	}

	dd, err1 := time.ParseDuration(doc)
	cd, err2 := time.ParseDuration(code)

	return err1 == nil && err2 == nil && dd == cd
}

// checkDocRows returns one problem per row: an unrecognized name, a literal
// default with no code value to compare to, or a literal that differs.
func checkDocRows(
	rows []docRow, recognized map[string]struct{}, cfg *config.Config, accessors map[string]func(*config.Config) string,
) []string {
	byEnv := config.DefaultsByEnvVar(cfg)

	var problems []string

	for _, row := range rows {
		where := row.file + ":" + strconv.Itoa(row.line)

		if _, ok := recognized[row.name]; !ok {
			problems = append(problems, where+": "+row.name+" is documented but no code binds it")

			continue
		}

		m := literalRe.FindStringSubmatch(row.def)
		if m == nil {
			continue
		}

		var code string

		if accessor, ok := accessors[row.name]; ok {
			code = accessor(cfg)
		} else if v, ok := byEnv[row.name]; ok {
			code = v
		} else {
			problems = append(problems, where+": add an accessor for "+row.name+" in docs_defaults_test.go")

			continue
		}

		if !sameValue(m[1], code) {
			problems = append(problems, where+": "+row.name+" documents default "+m[1]+" but the code default is "+code)
		}
	}

	return problems
}

// loadDefaultConfig runs config.Load with no SP_* variable set.
func loadDefaultConfig(t *testing.T) *config.Config {
	t.Helper()

	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "SP_") {
			require.NoError(t, os.Unsetenv(name))
			t.Cleanup(func() { _ = os.Setenv(name, value) })
		}
	}

	cfg, err := config.Load()
	require.NoError(t, err)

	return cfg
}

func docsRepoRoot(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)

	// <root>/server/internal/envcheck/docs_defaults_test.go
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
}

//nolint:paralleltest // unsets SP_* variables in the process environment
func TestDocumentedDefaultsMatchCode(t *testing.T) {
	root := docsRepoRoot(t)

	files, err := filepath.Glob(filepath.Join(root, "web", "docs", "docs", "configuration", "*.md"))
	require.NoError(t, err)
	require.NotEmpty(t, files)

	files = append(files, filepath.Join(root, "wiki", "conventions", "runners.md"))

	rows := make([]docRow, 0, len(files))

	for _, f := range files {
		content, err := os.ReadFile(f)
		require.NoError(t, err)

		rel, _ := filepath.Rel(root, f)
		rows = append(rows, parseDocTables(rel, string(content))...)
	}

	require.NotEmpty(t, rows)

	cfg := loadDefaultConfig(t)
	problems := checkDocRows(rows, recognizedEnvVars(), cfg, docsDefaultAccessors)
	sort.Strings(problems)
	require.Empty(t, problems, strings.Join(problems, "\n"))
}

// TestDocsDefaultsCheckerPositiveControl proves the comparator flags what it is
// meant to flag, so a clean real run means something.
func TestDocsDefaultsCheckerPositiveControl(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.Server.CheckWorker.Nb = 25

	recognized := map[string]struct{}{
		"SP_SERVER_CHECK_WORKER_NB": {},
		"SP_NO_ACCESSOR":            {},
	}

	md := "| Env var | Default | What |\n|---|---|---|\n" +
		"| `SP_SERVER_CHECK_WORKER_NB` | `3` | x |\n" +
		"| `SP_SERVER_JOB_WORKER_FETCH_MAX_AHEAD` | `5m` | x |\n" +
		"| `SP_NO_ACCESSOR` | `1` | x |\n" +
		"| `SP_NO_ACCESSOR` | derived | x |\n"
	rows := parseDocTables("fixture.md", md)
	require.Len(t, rows, 4)

	problems := checkDocRows(rows, recognized, cfg, docsDefaultAccessors)
	require.Len(t, problems, 3)
	require.Contains(t, problems[0], "documents default 3")
	require.Contains(t, problems[0], "code default is 25")
	require.Contains(t, problems[1], "SP_SERVER_JOB_WORKER_FETCH_MAX_AHEAD")
	require.Contains(t, problems[2], "add an accessor for SP_NO_ACCESSOR")
}

func TestDocsDefaultsNormalization(t *testing.T) {
	t.Parallel()

	require.True(t, sameValue("5m", "5m0s"))
	require.False(t, sameValue("30s", "15s"))
	require.False(t, sameValue("true", "false"))
	require.True(t, sameValue("25", "25"))
}
