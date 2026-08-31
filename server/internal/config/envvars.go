package config

import (
	"sort"
	"strings"

	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
)

// RecognizedEnvVars returns every SP_*-prefixed environment variable name that
// config.Load actually binds. It is the union of two sources:
//
//  1. koanf-reachable struct paths — reflected from the Config struct. koanf's
//     env provider lowercases an SP_* name and turns every underscore into a
//     dot, so a variable can only ever reach a leaf path whose every segment is
//     a single (underscore-free) word. Paths with a snake_case segment are
//     unreachable via env and are excluded here; they must be earned through a
//     manual reader (source 2) or a systemconfig entry instead.
//  2. Manual readers in config.Load — the apply*Env helpers and the bare
//     os.Getenv reads that bind the snake_case-tagged (or koanf:"-") settings
//     koanf's env loader cannot reach.
//
// "Recognized" means *actually binds*: a name that parses into the koanf map but
// never lands on a struct field (e.g. SP_ENCRYPTION_MASTER_KEY →
// encryption.master.key vs the master_key tag) is deliberately absent, so the
// startup env check flags it instead of letting a typo become a silent no-op.
func RecognizedEnvVars() []string {
	set := make(map[string]struct{})
	for _, name := range koanfReachableEnvVars() {
		set[name] = struct{}{}
	}

	for _, name := range manualReaderEnvVars() {
		set[name] = struct{}{}
	}

	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}

	sort.Strings(out)

	return out
}

// koanfReachableEnvVars reflects the Config struct through the same koanf
// structs provider config.Load uses for its defaults and reverse-maps every
// leaf path to the SP_* name that would reach it. The structs provider honors
// `koanf:"-"` (LogLevel, Server.Redirects, App.EnableBugReport never appear) and
// emits every leaf regardless of value, so a zero-valued Config yields the full
// path set — there is no need to reconstruct the defaults literal here.
func koanfReachableEnvVars() []string {
	k := koanf.New(".")
	// The in-memory structs provider cannot fail in practice.
	_ = k.Load(structs.Provider(Config{}, "koanf"), nil)

	keys := k.Keys()
	out := make([]string, 0, len(keys))
	for _, path := range keys {
		if name, ok := envNameForKoanfPath(path); ok {
			out = append(out, name)
		}
	}

	return out
}

// envNameForKoanfPath returns the SP_* environment variable name that koanf's
// env provider would map onto the given dotted leaf path, and ok=false when no
// such name exists. Because the provider collapses every underscore to a dot, a
// path is reachable via env iff each of its segments is a single underscore-free
// word: SP_A_B_C can only ever produce a.b.c, never a.b_c.
func envNameForKoanfPath(path string) (string, bool) {
	for _, segment := range strings.Split(path, ".") {
		if strings.Contains(segment, "_") {
			return "", false
		}
	}

	return "SP_" + strings.ToUpper(strings.ReplaceAll(path, ".", "_")), true
}

// manualReaderEnvVars lists every SP_* name read directly via os.Getenv in
// config.Load and its apply*Env helpers. koanf's env provider collapses
// underscores to dots, so these snake_case-tagged (or koanf:"-") settings are
// bound by hand and would be invisible to the reflection-derived set above.
// Keep this in sync with the apply*Env helpers and the bare os.Getenv reads in
// config.Load — TestManualReaderEnvVarsBind spot-checks that these names bind.
func manualReaderEnvVars() []string {
	return append(manualReaderServerEnvVars(), manualReaderPlatformEnvVars()...)
}

// manualReaderServerEnvVars covers Load's own bare reads and the helpers that
// configure request serving: rate limiting, password hashing, jobs, realtime,
// agents, auth, the server hosts (docs / custom-domain CNAME), in-server ACME
// and check scheduling.
func manualReaderServerEnvVars() []string {
	return []string{
		// Bare reads in Load itself.
		"SP_REDIRECTS",
		"SP_RUN_MODE",
		"SP_REGION",
		// Entitlements runaway caps — underscores collapse to dots under the
		// env provider, so Load reads these by hand (see envInt calls).
		"SP_ENTITLEMENTS_SMS_RUNAWAY_PER_HOUR",
		"SP_ENTITLEMENTS_CALL_RUNAWAY_PER_HOUR",
		"SP_SHUTDOWN_TIMEOUT",
		"SP_SERVER_MAX_REQUEST_DURATION",
		"SP_DB_RESET",
		"SP_LOG_LEVEL",
		"SP_APP_GITHUB_ISSUES_TOKEN",
		"SP_APP_GITHUB_REPO",
		// applyCheckersEnv — both keys have a snake_case segment, so koanf's
		// env loader cannot reach them (see BrowserCheckerConfig).
		"SP_CHECKERS_BROWSER_CDP_URL",
		"SP_CHECKERS_BROWSER_CHROME_PATH",
		// applyRateLimitingEnv
		"SP_SERVER_RATE_LIMITING_REQUESTS_PER_MINUTE",
		"SP_SERVER_RATE_LIMITING_BURST",
		"SP_SERVER_RATE_LIMITING_MAX_CONCURRENT",
		"SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES",
		"SP_SERVER_RATE_LIMITING_TOKEN_BUCKETS_PER_IP",
		"SP_SERVER_RATE_LIMITING_RATE_QUEUE",
		"SP_SERVER_RATE_LIMITING_CONCURRENCY_QUEUE",
		"SP_SERVER_RATE_LIMITING_MAX_QUEUE_WAIT",
		// applyPasswordHashingEnv
		"SP_AUTH_PASSWORD_ALGORITHM",
		"SP_AUTH_PASSWORD_ARGON2_MEMORY",
		"SP_AUTH_PASSWORD_ARGON2_TIME",
		"SP_AUTH_PASSWORD_ARGON2_THREADS",
		"SP_AUTH_PASSWORD_ARGON2_KEY_LENGTH",
		"SP_AUTH_PASSWORD_ARGON2_SALT_LENGTH",
		"SP_AUTH_PASSWORD_BCRYPT_COST",
		"SP_AUTH_PASSWORD_REHASH_ON_LOGIN",
		// applyAuditEnv — every audit key is snake_case, so koanf's env loader
		// cannot reach any of them (see AuditConfig).
		"SP_AUDIT_CAPTURE_IP",
		"SP_AUDIT_RETENTION_DAYS",
		"SP_AUDIT_FAILED_LOGIN_FOLD_WINDOW_MINUTES",
		"SP_AUDIT_FAILED_LOGIN_MAX_PER_ORG_PER_HOUR",
		// applyJobsEnv
		"SP_JOBS_STUCK_TIMEOUT",
		"SP_JOBS_REAPER_INTERVAL",
		// applyRealtimeEnv
		"SP_REALTIME_FLUSH_INTERVAL",
		"SP_REALTIME_PING_INTERVAL",
		"SP_REALTIME_MAX_CONNECTIONS",
		"SP_REALTIME_MAX_SUBSCRIPTIONS_PER_CONNECTION",
		// applyAgentEnv
		"SP_AGENT_SERVER_URL",
		"SP_AGENT_ENROLLMENT_TOKEN",
		"SP_AGENT_KEYS_FILE",
		"SP_AGENT_KEYS",
		"SP_AGENT_NAME",
		"SP_AGENT_PRINT_KEYS",
		// applyAuthEnv
		"SP_AUTH_ACCESS_TOKEN_EXPIRY",
		"SP_AUTH_REFRESH_TOKEN_EXPIRY",
		// applyServerEnv
		"SP_SERVER_DOCS_HOST",
		"SP_DOCS_HOST",
		"SP_SERVER_CUSTOM_DOMAIN_CNAME_TARGET",
		"SP_CUSTOM_DOMAIN_CNAME_TARGET",
		"SP_SERVER_CUSTOM_DOMAIN_CNAME_MODE",
		"SP_CUSTOM_DOMAIN_CNAME_MODE",
		"SP_SERVER_EXIT_WITH_PARENT",
		"SP_EXIT_WITH_PARENT",
		"SP_SERVER_CORS_ALLOWED_ORIGINS",
		"SP_CORS_ALLOWED_ORIGINS",
		// applyACMEEnv (acme.enabled / acme.email are koanf-reachable)
		"SP_ACME_CA_URL",
		"SP_ACME_LISTEN_HTTP",
		"SP_ACME_LISTEN_HTTPS",
		"SP_ACME_PROXY_PROTOCOL",
		"SP_ACME_PROXY_PROTOCOL_TRUSTED_CIDRS",
		"SP_ACME_FALLBACK_UPSTREAM_HTTPS",
		"SP_ACME_FALLBACK_UPSTREAM_HTTP",
		"SP_ACME_FALLBACK_UPSTREAM_PROXY_PROTOCOL",
		// applyEncryptionEnv — encryption.master_key and master_key_file are
		// snake_case, so koanf's env loader lands on encryption.master.key and
		// binds nothing. Absent from this list the credentials service stayed in
		// plaintext mode while telling the operator to set the very name it then
		// reported as unrecognized.
		"SP_ENCRYPTION_MASTER_KEY",
		"SP_ENCRYPTION_MASTER_KEY_FILE",
		"SP_ENCRYPTION_AUTO_MIGRATE",
		// applySchedulingEnv
		"SP_SCHEDULING_SLOW_THRESHOLD_MS",
		"SP_SCHEDULING_CHECK_TIMEOUT_MS",
		"SP_SCHEDULING_TIER_CREDIT_SECONDS",
		"SP_SCHEDULING_TIER_CREDIT_MAX_SECONDS",
		"SP_SCHEDULING_COST_TIMEOUT_FACTOR",
		"SP_SCHEDULING_COST_TIMEOUT_FLOOR_MS",
		"SP_SCHEDULING_LANE_SLOW_THRESHOLD_MS",
		"SP_SCHEDULING_LANE_FAST_THRESHOLD_MS",
		"SP_SCHEDULING_FAST_LANE_RESERVED",
	}
}

// manualReaderPlatformEnvVars covers the helpers that configure the process
// platform rather than request handling: profiler, database pool, Go runtime,
// file storage and web push.
func manualReaderPlatformEnvVars() []string {
	names := []string{
		// applyProfilerEnv
		"SP_PROFILER_BLOCK_RATE",
		"SP_PROFILER_MUTEX_FRACTION",
		// applyDatabasePoolEnv
		"SP_DB_MAX_OPEN_CONNS",
		"SP_DB_MAX_IDLE_CONNS",
		"SP_DB_CONN_MAX_LIFETIME",
		"SP_DB_CONN_MAX_IDLE_TIME",
		// applyDBSlowQueryEnv
		"SP_DB_SLOW_QUERY_THRESHOLD",
		// applyMigrationGuardModeEnv
		"SP_DB_MIGRATION_GUARD_MODE",
		// applyRuntimeEnv
		"SP_RUNTIME_MEMORY_LIMIT",
		"SP_RUNTIME_AUTO_MEMORY_LIMIT",
		"SP_RUNTIME_MEMORY_LIMIT_RATIO",
		"SP_RUNTIME_GC_PERCENT",
		// applyFileStorageEnv
		"SP_FILESTORAGE_LOCAL_ROOT",
		"SP_FILESTORAGE_S3_BUCKET",
		"SP_FILESTORAGE_S3_REGION",
		"SP_FILESTORAGE_S3_PREFIX",
		"SP_FILESTORAGE_S3_ENDPOINT",
		"SP_FILESTORAGE_S3_USE_PATH_STYLE",
		"SP_FILESTORAGE_S3_ACCESS_KEY",
		"SP_FILESTORAGE_S3_SECRET_KEY",
		// applyWebPushEnv
		"SP_WEBPUSH_VAPID_PUBLIC_KEY",
		"SP_WEBPUSH_VAPID_PRIVATE_KEY",
		"SP_WEBPUSH_SUBJECT",
		"SP_WEBPUSH_ENABLED",
		// applyPostHogEnv — posthog.enabled / posthog.host are koanf-reachable
		// and come from the reflection set; these two have snake_case segments.
		"SP_POSTHOG_PROJECT_API_KEY",
		"SP_POSTHOG_PERSONAL_API_KEY",
		// applySentryEnv — sentry.dsn / sentry.environment / sentry.debug are
		// koanf-reachable (single-word segments) and come from the reflection
		// set; only traces_sample_rate has a snake_case segment.
		"SP_SENTRY_TRACES_SAMPLE_RATE",
	}

	// applyWhatsAppEnv — whatsapp.enabled is koanf-reachable and comes from the
	// reflection set; every other WhatsApp key has a snake_case segment. The
	// names come from the same list the reader iterates, so the two cannot drift.
	// applyEntitlementsEnv contributes the WhatsApp runaway cap.
	// applyTelegramEnv — same quirk: telegram.enabled is koanf-reachable, every
	// other Telegram key (bot_token, bot_username, webhook_secret, base_url) has
	// a snake_case segment. applyEntitlementsEnv contributes the runaway caps.
	// applySMSEnv / applyVoiceEnv — same quirk again, and more thoroughly:
	// almost every SP_SMS_*/SP_VOICE_* key has a snake_case segment
	// (SP_SMS_TWILIO_ACCOUNT_SID would land on sms.twilio.account.sid), so the
	// whole block is bound by hand and listed here.
	whatsAppNames := WhatsAppEnvVarNames()
	telegramNames := TelegramEnvVarNames()
	smsNames := AllSMSEnvVarNames()
	out := make([]string, 0, len(names)+len(whatsAppNames)+len(telegramNames)+len(smsNames)+2)
	out = append(out, names...)
	out = append(out, whatsAppNames...)
	out = append(out, EnvEntitlementsWhatsAppRunaway)
	out = append(out, telegramNames...)
	out = append(out, EnvEntitlementsTelegramRunaway)
	out = append(out, smsNames...)

	return out
}
