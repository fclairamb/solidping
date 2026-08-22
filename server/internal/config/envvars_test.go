package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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
	r.Contains(set, "SP_DB_SLOW_QUERY_THRESHOLD")
	r.Contains(set, "SP_DB_MIGRATION_GUARD_MODE")
	r.Contains(set, "SP_SENTRY_TRACES_SAMPLE_RATE")

	// Confirmed-broken bindings must NOT be recognized (see spec open question):
	// SP_ENCRYPTION_MASTER_KEY transforms to encryption.master.key but the tag
	// is master_key, so it never binds. Adding it here would paper over the bug.
	r.NotContains(set, "SP_ENCRYPTION_MASTER_KEY")
	r.NotContains(set, "SP_ENCRYPTION_MASTER_KEY_FILE")
	r.NotContains(set, "SP_ENCRYPTION_AUTO_MIGRATE")
	// The typo must not be recognized (it is the near-miss the check warns on).
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
	t.Setenv("SP_DB_SLOW_QUERY_THRESHOLD", "250ms")
	t.Setenv("SP_DB_MIGRATION_GUARD_MODE", "warn")

	r := require.New(t)
	cfg, err := Load()
	r.NoError(err)
	r.Equal(7, cfg.Server.RateLimiting.TrustedProxies)
	r.Equal("docs.example.test", cfg.Server.DocsHost)
	r.Equal(3*60, int(cfg.Jobs.ReaperInterval.Seconds()))
	r.Equal(1234, cfg.Realtime.MaxConnections)
	r.Equal(42, cfg.Entitlements.SMSRunawayPerHour)
	r.Equal(7, cfg.Entitlements.CallRunawayPerHour)
	r.Equal(250*time.Millisecond, cfg.Database.SlowQueryThreshold)
	r.Equal(MigrationGuardModeWarn, cfg.Database.MigrationGuardMode)
}

// TestDBSlowQueryThresholdDefaultsTo500ms pins the documented default and
// proves it survives Load() with no env override — a successful query below
// this must stay at DEBUG, at or above it must WARN.
func TestDBSlowQueryThresholdDefaultsTo500ms(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg, err := Load()
	r.NoError(err)
	r.Equal(500*time.Millisecond, cfg.Database.SlowQueryThreshold)
}

// TestDBSlowQueryThresholdZeroDisables proves SP_DB_SLOW_QUERY_THRESHOLD=0
// binds through to the documented "0 disables" sentinel rather than being
// treated as "unset" and falling back to the default.
func TestDBSlowQueryThresholdZeroDisables(t *testing.T) {
	t.Setenv("SP_DB_SLOW_QUERY_THRESHOLD", "0s")

	r := require.New(t)

	cfg, err := Load()
	r.NoError(err)
	r.Zero(cfg.Database.SlowQueryThreshold)
}

// TestMigrationGuardModeDefaultsToStrict pins the documented default:
// blocking stays the default, with no env override needed to get it.
func TestMigrationGuardModeDefaultsToStrict(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg, err := Load()
	r.NoError(err)
	r.Equal(MigrationGuardModeStrict, cfg.Database.MigrationGuardMode)
	r.NoError(cfg.Validate())
}

// TestMigrationGuardModeInvalidValueFailsValidation proves an unrecognized
// SP_DB_MIGRATION_GUARD_MODE value is a config validation error rather than a
// silent fallback to strict or warn.
func TestMigrationGuardModeInvalidValueFailsValidation(t *testing.T) {
	t.Setenv("SP_DB_MIGRATION_GUARD_MODE", "yolo")

	r := require.New(t)

	cfg, err := Load()
	r.NoError(err)
	r.Equal("yolo", cfg.Database.MigrationGuardMode, "the bad value must survive Load so Validate can reject it")
	r.ErrorIs(cfg.Validate(), ErrInvalidMigrationGuardMode)
}

// TestExitWithParentEnvVarBinds proves SP_EXIT_WITH_PARENT both lands on its
// struct field AND is advertised as recognized. The second half is the point:
// server.exit_with_parent has a snake_case segment, so it is excluded from the
// koanf-reflected set by construction and must be earned through the manual
// list. Without the list entry the variable still worked, but every e2e run —
// and every operator following the documented row — got a spurious
// "unrecognized SP_* environment variable" warning at startup, which is how
// startup warnings get trained into noise (spec 2026-08-12-05).
func TestExitWithParentEnvVarBinds(t *testing.T) {
	t.Setenv("SP_EXIT_WITH_PARENT", "true")

	r := require.New(t)

	cfg, err := Load()
	r.NoError(err)
	r.True(cfg.Server.ExitWithParent)

	recognized := RecognizedEnvVars()
	r.Contains(recognized, "SP_EXIT_WITH_PARENT")
	r.Contains(recognized, "SP_SERVER_EXIT_WITH_PARENT")
}

// TestExitWithParentPrefersTheServerScopedName pins the precedence shared by
// every other dual-name pair in applyServerEnv.
func TestExitWithParentPrefersTheServerScopedName(t *testing.T) {
	t.Setenv("SP_SERVER_EXIT_WITH_PARENT", "false")
	t.Setenv("SP_EXIT_WITH_PARENT", "true")

	r := require.New(t)

	cfg, err := Load()
	r.NoError(err)
	r.False(cfg.Server.ExitWithParent, "the server-scoped name wins")
}

// TestEverySPEnvVarReadIsRecognized is the general guard the single-case tests
// above cannot be: it scans this package's own source for every literal
// os.Getenv("SP_…") call and requires each name to be advertised by
// RecognizedEnvVars.
//
// It exists because the previous coverage was circular — the old test iterated
// the registry list, so a name missing from the list was untested by
// construction, which is exactly how SP_EXIT_WITH_PARENT shipped unregistered.
// The one thing this cannot see is a name read through a variable rather than a
// literal, so manual readers spell their names out (see applyServerEnv).
func TestEverySPEnvVarReadIsRecognized(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	entries, err := os.ReadDir(".")
	r.NoError(err)

	pattern := regexp.MustCompile(`os\.Getenv\("(SP_[A-Z0-9_]+)"\)`)
	recognized := RecognizedEnvVars()
	seen := map[string]struct{}{}

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		source, readErr := os.ReadFile(name)
		r.NoError(readErr)

		for _, match := range pattern.FindAllStringSubmatch(string(source), -1) {
			if _, done := seen[match[1]]; done {
				continue
			}

			seen[match[1]] = struct{}{}

			r.Contains(recognized, match[1],
				"%s is read by config.Load but is not advertised by RecognizedEnvVars: "+
					"add it to manualReaderEnvVars, or the startup env check will warn about a "+
					"variable that in fact binds", match[1])
		}
	}

	r.NotEmpty(seen, "the scan must actually find os.Getenv calls")
}

// TestWhatsAppEnvVarsBind proves every SP_WHATSAPP_* name in the manual-reader
// list actually lands on its struct field. Every one of these keys except
// `enabled` has a snake_case koanf tag that koanf's env provider can never
// reach on its own (SP_WHATSAPP_PHONE_NUMBER_ID collapses to
// whatsapp.phone.number.id), so without applyWhatsAppEnv they would be silent
// no-ops — the failure mode this test exists to catch.
func TestWhatsAppEnvVarsBind(t *testing.T) {
	t.Setenv("SP_WHATSAPP_ENABLED", "true")
	t.Setenv("SP_WHATSAPP_ACCESS_TOKEN", "tok-123")
	t.Setenv("SP_WHATSAPP_PHONE_NUMBER_ID", "555000111")
	t.Setenv("SP_WHATSAPP_WABA_ID", "waba-1")
	t.Setenv("SP_WHATSAPP_APP_SECRET", "sekrit")
	t.Setenv("SP_WHATSAPP_WEBHOOK_VERIFY_TOKEN", "verify-me")
	t.Setenv("SP_WHATSAPP_API_VERSION", "v99.0")
	t.Setenv("SP_WHATSAPP_ALERT_TEMPLATE", "custom_alert")
	t.Setenv("SP_WHATSAPP_VERIFY_TEMPLATE", "custom_verify")
	t.Setenv("SP_WHATSAPP_TEMPLATE_LANGUAGE", "fr")
	t.Setenv("SP_WHATSAPP_BASE_URL", "https://graph.test")
	t.Setenv("SP_ENTITLEMENTS_WHATSAPP_RUNAWAY_PER_HOUR", "11")

	r := require.New(t)
	cfg, err := Load()
	r.NoError(err)

	r.True(cfg.WhatsApp.Enabled)
	r.Equal("tok-123", cfg.WhatsApp.AccessToken)
	r.Equal("555000111", cfg.WhatsApp.PhoneNumberID)
	r.Equal("waba-1", cfg.WhatsApp.WABAID)
	r.Equal("sekrit", cfg.WhatsApp.AppSecret)
	r.Equal("verify-me", cfg.WhatsApp.WebhookVerifyToken)
	r.Equal("v99.0", cfg.WhatsApp.ResolvedAPIVersion())
	r.Equal("custom_alert", cfg.WhatsApp.ResolvedAlertTemplate())
	r.Equal("custom_verify", cfg.WhatsApp.ResolvedVerifyTemplate())
	r.Equal("fr", cfg.WhatsApp.ResolvedTemplateLanguage())
	r.Equal("https://graph.test", cfg.WhatsApp.BaseURL)
	r.Equal(11, cfg.Entitlements.WhatsAppRunawayPerHour)
	r.True(cfg.WhatsApp.Active())

	// Every name used above must also be advertised as recognized, otherwise
	// the startup env check would flag a variable that in fact binds.
	recognized := RecognizedEnvVars()
	for _, name := range []string{
		"SP_WHATSAPP_ENABLED", "SP_WHATSAPP_ACCESS_TOKEN", "SP_WHATSAPP_PHONE_NUMBER_ID",
		"SP_WHATSAPP_WABA_ID", "SP_WHATSAPP_APP_SECRET", "SP_WHATSAPP_WEBHOOK_VERIFY_TOKEN",
		"SP_WHATSAPP_API_VERSION", "SP_WHATSAPP_ALERT_TEMPLATE", "SP_WHATSAPP_VERIFY_TEMPLATE",
		"SP_WHATSAPP_TEMPLATE_LANGUAGE", "SP_WHATSAPP_BASE_URL",
		"SP_ENTITLEMENTS_WHATSAPP_RUNAWAY_PER_HOUR",
	} {
		r.Contains(recognized, name)
	}
}

// TestWhatsAppDefaultsOff proves the feature is dark out of the box and that
// the kill switch alone cannot turn it on.
func TestWhatsAppDefaultsOff(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg, err := Load()
	r.NoError(err)
	r.False(cfg.WhatsApp.Enabled)
	r.False(cfg.WhatsApp.Active())
	r.Equal(DefaultWhatsAppAPIVersion, cfg.WhatsApp.ResolvedAPIVersion())
	r.Equal(DefaultWhatsAppAlertTemplate, cfg.WhatsApp.ResolvedAlertTemplate())
	r.Equal(DefaultWhatsAppVerifyTemplate, cfg.WhatsApp.ResolvedVerifyTemplate())
	r.Equal(DefaultWhatsAppTemplateLanguage, cfg.WhatsApp.ResolvedTemplateLanguage())

	// Enabled without credentials is still off.
	switchOnly := WhatsAppConfig{Enabled: true}
	r.False(switchOnly.Active())

	tokenOnly := WhatsAppConfig{Enabled: true, AccessToken: "t"}
	r.False(tokenOnly.Active())

	idOnly := WhatsAppConfig{Enabled: true, PhoneNumberID: "1"}
	r.False(idOnly.Active())

	complete := WhatsAppConfig{Enabled: true, AccessToken: "t", PhoneNumberID: "1"}
	r.True(complete.Active())
}

// TestTelegramEnvVarsBind proves every SP_TELEGRAM_* name in the manual-reader
// list actually lands on its struct field. Each of these keys has a snake_case
// koanf tag that koanf's env provider can never reach on its own
// (SP_TELEGRAM_BOT_TOKEN collapses to telegram.bot.token), and `enabled` is
// deliberately dropped from that provider, so without applyTelegramEnv they
// would all be silent no-ops — the failure mode this test exists to catch.
func TestTelegramEnvVarsBind(t *testing.T) {
	t.Setenv("SP_TELEGRAM_ENABLED", "true")
	t.Setenv("SP_TELEGRAM_BOT_TOKEN", "123456789:AAtoken")
	t.Setenv("SP_TELEGRAM_BOT_USERNAME", "@solidping_bot")
	t.Setenv("SP_TELEGRAM_WEBHOOK_SECRET", "a-long-random-secret")
	t.Setenv("SP_TELEGRAM_BASE_URL", "https://telegram.test")
	t.Setenv("SP_ENTITLEMENTS_TELEGRAM_RUNAWAY_PER_HOUR", "13")

	r := require.New(t)
	cfg, err := Load()
	r.NoError(err)

	r.NotNil(cfg.Telegram.Enabled, "an explicit SP_TELEGRAM_ENABLED must bind to the tri-state pointer")
	r.True(*cfg.Telegram.Enabled)
	r.True(cfg.Telegram.IsEnabled())
	r.Equal("123456789:AAtoken", cfg.Telegram.BotToken)
	r.Equal("@solidping_bot", cfg.Telegram.BotUsername)
	// The '@' is stripped for link building: operators paste it both ways.
	r.Equal("solidping_bot", cfg.Telegram.ResolvedBotUsername())
	r.Equal("a-long-random-secret", cfg.Telegram.WebhookSecret)
	r.Equal("https://telegram.test", cfg.Telegram.ResolvedBaseURL())
	r.Equal(13, cfg.Entitlements.TelegramRunawayPerHour)
	r.True(cfg.Telegram.Active())

	// Every name used above must also be advertised as recognized, otherwise
	// the startup env check would flag a variable that in fact binds.
	recognized := RecognizedEnvVars()
	for _, name := range []string{
		"SP_TELEGRAM_ENABLED", "SP_TELEGRAM_BOT_TOKEN", "SP_TELEGRAM_BOT_USERNAME",
		"SP_TELEGRAM_WEBHOOK_SECRET", "SP_TELEGRAM_BASE_URL",
		"SP_ENTITLEMENTS_TELEGRAM_RUNAWAY_PER_HOUR",
	} {
		r.Contains(recognized, name)
	}
}

// TestTelegramActiveRequiresUsername pins the connect-surface rule: a bot token
// alone is not enough for Active(). Without a username the dashboard cannot
// build a connect link, so the connect surface would be half-on.
func TestTelegramActiveRequiresUsername(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.False((&TelegramConfig{Enabled: BoolPtr(true), BotToken: "t"}).Active())
	r.False((&TelegramConfig{Enabled: BoolPtr(true), BotUsername: "u"}).Active())
	r.False((&TelegramConfig{Enabled: BoolPtr(false), BotToken: "t", BotUsername: "u"}).Active())
	r.True((&TelegramConfig{Enabled: BoolPtr(true), BotToken: "t", BotUsername: "u"}).Active())

	// The default base URL is the real Bot API, and a trailing slash is trimmed
	// so the client never builds "…//bot<token>/…".
	r.Equal(DefaultTelegramBaseURL, (&TelegramConfig{}).ResolvedBaseURL())
	r.Equal("https://proxy.test", (&TelegramConfig{BaseURL: "https://proxy.test/"}).ResolvedBaseURL())
}

// TestTelegramEnabledIsTriState pins the switch semantics that make a bare bot
// token sufficient: unset means "on iff a token is present", so supplying the
// token IS the intent to enable. false stays an unconditional kill switch, and
// true stays explicit-on.
func TestTelegramEnabledIsTriState(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// nil (unset) — the default. Auto.
	r.False((&TelegramConfig{}).IsEnabled(), "unset with no token is off")
	r.True((&TelegramConfig{BotToken: "123:AAt"}).IsEnabled(),
		"a bot token alone must switch the feature on")
	r.True((&TelegramConfig{BotToken: "123:AAt"}).Configured())
	r.False((&TelegramConfig{BotToken: "  "}).IsEnabled(), "whitespace is not a token")

	// false — the kill switch beats a perfectly good token.
	off := &TelegramConfig{Enabled: BoolPtr(false), BotToken: "123:AAt", BotUsername: "u"}
	r.False(off.IsEnabled())
	r.False(off.Configured())
	r.False(off.Active())

	// true — explicit on, but still needs a token to do anything.
	on := &TelegramConfig{Enabled: BoolPtr(true)}
	r.True(on.IsEnabled())
	r.False(on.Configured(), "explicitly enabled without a token is still nothing")
}

// TestTelegramTokenAloneEnablesViaLoad is the end-to-end control for the
// tri-state default: with SP_TELEGRAM_BOT_TOKEN as the ONLY Telegram variable,
// a real Load() must come back enabled and configured, with Enabled still nil
// (unset) — otherwise the "auto" branch would be unreachable dead code.
//
// Not parallel: it manipulates the process environment.
func TestTelegramTokenAloneEnablesViaLoad(t *testing.T) {
	// LookupEnv, not Getenv: an exported-but-empty SP_TELEGRAM_ENABLED is a
	// different situation from an absent one, and Getenv cannot tell them apart
	// — the guard would silently turn into a failure instead of a skip.
	if _, present := os.LookupEnv(EnvTelegramEnabled); present {
		t.Skip(EnvTelegramEnabled + " is set in the ambient environment")
	}

	t.Setenv(EnvTelegramBotToken, "123456789:AAtoken")

	r := require.New(t)
	cfg, err := Load()
	r.NoError(err)

	r.Nil(cfg.Telegram.Enabled, "the default must stay unset (auto), never false")
	r.True(cfg.Telegram.IsEnabled(), "a bot token alone must be enough")
	r.True(cfg.Telegram.Configured())
	r.False(cfg.Telegram.Active(), "…but the connect surface waits for the derived username")
	r.Empty(cfg.Telegram.BotUsername)
	r.Empty(cfg.Telegram.WebhookSecret)
}

// TestTelegramEnabledEmptyIsUnsetNotFalse is the regression test for the dotenv
// hazard: `SP_TELEGRAM_ENABLED=` (present but empty) is exactly what a copied
// .env.example produces under `set -a; . .env`, `docker run --env-file`, or a
// PaaS dotenv importer. koanf hands mapstructure an empty string and the
// weakly-typed string→bool conversion turns it into FALSE, which for a
// tri-state pointer means an explicit kill switch — silently disabling a
// perfectly configured bot. An empty value means "I did not choose".
//
// Not parallel: it manipulates the process environment.
func TestTelegramEnabledEmptyIsUnsetNotFalse(t *testing.T) {
	r := require.New(t)

	for name, raw := range map[string]string{
		"empty":      "",
		"whitespace": "   ",
	} {
		t.Setenv(EnvTelegramEnabled, raw)
		t.Setenv(EnvTelegramBotToken, "123456789:AAtoken")

		cfg, err := Load()
		r.NoError(err, name)

		r.Nil(cfg.Telegram.Enabled, "%s must be treated as unset, never as false", name)
		r.True(cfg.Telegram.IsEnabled(), "%s must not disable a configured bot", name)
		r.True(cfg.Telegram.Configured(), "%s must not disable a configured bot", name)
	}

	// Garbage is not a kill switch either: it falls back to auto and warns.
	t.Setenv(EnvTelegramEnabled, "yes-please")
	t.Setenv(EnvTelegramBotToken, "123456789:AAtoken")

	cfg, err := Load()
	r.NoError(err)
	r.Nil(cfg.Telegram.Enabled)
	r.True(cfg.Telegram.Configured())

	// Positive control: an explicit false IS still honored, or the fix above
	// would have quietly removed the kill switch.
	t.Setenv(EnvTelegramEnabled, "false")

	cfg, err = Load()
	r.NoError(err)
	r.NotNil(cfg.Telegram.Enabled)
	r.False(*cfg.Telegram.Enabled)
	r.False(cfg.Telegram.IsEnabled(), "an explicit false must still kill the feature")
	r.False(cfg.Telegram.Configured())

	// …and so is an explicit true.
	t.Setenv(EnvTelegramEnabled, "true")

	cfg, err = Load()
	r.NoError(err)
	r.NotNil(cfg.Telegram.Enabled)
	r.True(*cfg.Telegram.Enabled)
}

// TestTelegramEnabledEnvAbsentPreservesConfiguredValue is the guard for the
// other half of the manual reader: because the env provider now DROPS
// telegram.enabled outright, applyTelegramEnabledEnv is its only writer — so an
// absent variable must leave a config-file (or default) value exactly as it
// found it, rather than resetting everyone to auto.
//
// Not parallel: it reads the process environment, which the sequential
// t.Setenv tests in this file mutate.
//
//nolint:paralleltest // reads os.Environ; must not race the t.Setenv tests
func TestTelegramEnabledEnvAbsentPreservesConfiguredValue(t *testing.T) {
	if _, present := os.LookupEnv(EnvTelegramEnabled); present {
		t.Skip(EnvTelegramEnabled + " is set in the ambient environment")
	}

	r := require.New(t)

	off := TelegramConfig{Enabled: BoolPtr(false), BotToken: "123:AAt"}
	applyTelegramEnabledEnv(&off)
	r.NotNil(off.Enabled, "an absent env var must not clear a config-file kill switch")
	r.False(*off.Enabled)

	on := TelegramConfig{Enabled: BoolPtr(true)}
	applyTelegramEnabledEnv(&on)
	r.NotNil(on.Enabled)
	r.True(*on.Enabled)

	auto := TelegramConfig{BotToken: "123:AAt"}
	applyTelegramEnabledEnv(&auto)
	r.Nil(auto.Enabled)
}

// TestTelegramEnvExampleHasNoDisablingEmptyValues walks the shipped
// .env.example the way a dotenv importer would and proves that sourcing it with
// nothing but a bot token filled in leaves Telegram ON. The file tells the
// reader to copy it, so a bare `KEY=` line in it is a live configuration
// hazard, not a cosmetic one.
func TestTelegramEnvExampleHasNoDisablingEmptyValues(t *testing.T) {
	r := require.New(t)

	raw, err := os.ReadFile(filepath.Join("..", "..", "..", ".env.example"))
	r.NoError(err)

	var telegramVars []string

	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, found := strings.Cut(line, "=")
		if !found || !strings.HasPrefix(key, "SP_TELEGRAM_") {
			continue
		}

		telegramVars = append(telegramVars, key)
		t.Setenv(key, value)
	}

	r.NotEmpty(telegramVars, "the fixture must actually have found SP_TELEGRAM_* lines")
	r.NotContains(telegramVars, EnvTelegramEnabled,
		".env.example must not ship a bare SP_TELEGRAM_ENABLED= line: it parses as false, "+
			"not as unset, and would silently disable a perfectly good bot token")

	// Now the operator fills in the one required value.
	t.Setenv(EnvTelegramBotToken, "123456789:AAtoken")

	cfg, err := Load()
	r.NoError(err)
	r.True(cfg.Telegram.Configured(),
		"sourcing .env.example plus a bot token must leave Telegram enabled")
}

// TestTelegramConfiguredVsActive pins the split between "can we talk to the Bot
// API" and "can a user connect a chat". A token-only instance must be
// Configured (webhook route, escalation dispatch, bootstrap) while NOT being
// Active (connect surface stays off until the @username is known).
func TestTelegramConfiguredVsActive(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	tokenOnly := &TelegramConfig{Enabled: BoolPtr(true), BotToken: "123:AAt"}
	r.True(tokenOnly.Configured(), "a bot token alone is a usable bot identity")
	r.False(tokenOnly.Active(), "no @username yet, so no connect link can be built")

	full := &TelegramConfig{Enabled: BoolPtr(true), BotToken: "123:AAt", BotUsername: "solidping_bot"}
	r.True(full.Configured())
	r.True(full.Active())

	// No token: nothing at all, whatever else is set.
	usernameOnly := &TelegramConfig{Enabled: BoolPtr(true), BotUsername: "solidping_bot"}
	r.False(usernameOnly.Configured())
	r.False(usernameOnly.Active())

	// Whitespace is not a token.
	r.False((&TelegramConfig{Enabled: BoolPtr(true), BotToken: "   "}).Configured())
}
