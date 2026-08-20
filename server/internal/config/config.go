// Package config provides application configuration management using koanf.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"

	"github.com/fclairamb/solidping/server/internal/domainverify"
)

// Node role constants.
const (
	NodeRoleAll    = "all"
	NodeRoleAPI    = "api"
	NodeRoleJobs   = "jobs"
	NodeRoleChecks = "checks"
	// NodeRoleAgent runs the standard binary as a deported (org-scoped) check
	// agent (spec 2026-07-16-02): no database, no migrations, no HTTP server —
	// just the check worker loop over an outbound WebSocket to the master.
	NodeRoleAgent = "agent"
)

// Database connection-pool defaults. api/all nodes serve HTTP traffic and need
// the larger pool; checks/jobs nodes only run batched polling loops (ClaimJobs,
// result writes) and are typically deployed as several replicas sharing one
// Postgres role, so an API-sized pool per replica quickly saturates the role's
// rolconnlimit even at idle. See spec 2026-07-05-09.
const (
	dbPoolMaxOpenConnsDefault       = 25
	dbPoolMaxIdleConnsDefault       = 10
	dbPoolMaxOpenConnsChecksDefault = 8
	dbPoolMaxIdleConnsChecksDefault = 2
)

// Database type constants.
const (
	DatabaseTypePostgres         = "postgres"
	DatabaseTypePostgresEmbedded = "postgres-embedded"
	DatabaseTypeSQLite           = "sqlite"
	DatabaseTypeSQLiteMemory     = "sqlite-memory"
)

// Migration guard mode constants (db.migration_guard_mode /
// SP_DB_MIGRATION_GUARD_MODE). Strict is the default and the only mode
// production should run: a checksum mismatch fails the boot. Warn logs the
// mismatch and lets the boot continue — intended for local development only,
// where editing an already-applied migration's comment is common. See
// internal/db/migrationguard.
const (
	MigrationGuardModeStrict = "strict"
	MigrationGuardModeWarn   = "warn"
)

// Deployment mode constants. Drives the per-org entitlement defaults:
// self-hosted caps SSO membership, SaaS caps aggregate check rate.
const (
	DeploymentModeSelfHosted = "self-hosted"
	DeploymentModeSaaS       = "saas"
)

// ValidDeploymentModes returns the supported SP_DEPLOYMENT_MODE values.
func ValidDeploymentModes() []string {
	return []string{DeploymentModeSelfHosted, DeploymentModeSaaS}
}

// envTrue is the canonical string value that enables a boolean env var.
const envTrue = "true"

var (
	// ErrInvalidDatabaseType is returned when the database type is invalid.
	ErrInvalidDatabaseType = errors.New(
		"database type must be 'postgres', 'postgres-embedded', 'sqlite', or 'sqlite-memory'",
	)
	// ErrDatabaseURLRequired is returned when postgres is selected but URL is missing.
	ErrDatabaseURLRequired = errors.New("database URL is required for postgres")
	// ErrDatabaseDirRequired is returned when sqlite is selected but directory is missing.
	ErrDatabaseDirRequired = errors.New("database directory is required for sqlite")
	// ErrInvalidMigrationGuardMode is returned when the migration guard mode
	// is neither "strict" nor "warn".
	ErrInvalidMigrationGuardMode = errors.New(
		"database migration guard mode must be 'strict' or 'warn'",
	)
	// ErrInvalidNodeRole is returned when the node role is invalid.
	ErrInvalidNodeRole = errors.New(
		"node role must be 'all', 'api', 'jobs', 'checks' or 'agent', " +
			"or a comma-separated combination of 'api', 'jobs' and 'checks'",
	)
	// ErrExclusiveNodeRole is returned when a whole-node role ('all' or
	// 'agent') is combined with another role in a comma-separated list.
	ErrExclusiveNodeRole = errors.New("node role cannot be combined with other roles")
	// ErrAgentServerURLRequired is returned when role is "agent" but no server URL is set.
	ErrAgentServerURLRequired = errors.New("SP_AGENT_SERVER_URL is required when SP_NODE_ROLE is set to 'agent'")
	// ErrRegionRequiredForChecks is returned when role is "checks" but region is not set.
	ErrRegionRequiredForChecks = errors.New("SP_NODE_REGION is required when SP_NODE_ROLE is set to 'checks'")
	// ErrInvalidAggregationRetention is returned when an aggregation retention value is < 1.
	ErrInvalidAggregationRetention = errors.New("aggregation retention values must be >= 1")
	// ErrInvalidDeploymentMode is returned when SP_DEPLOYMENT_MODE is set to something other than "saas" / "self-hosted".
	ErrInvalidDeploymentMode = errors.New("deployment mode must be 'saas' or 'self-hosted'")
	// ErrInvalidPasswordAlgorithm is returned when auth.password.algorithm is not a supported algorithm.
	ErrInvalidPasswordAlgorithm = errors.New("password hashing algorithm must be 'argon2id' or 'bcrypt'")
	// ErrInvalidArgon2Params is returned when an argon2id cost parameter is below its hard floor.
	ErrInvalidArgon2Params = errors.New("invalid argon2id parameters")
	// ErrInvalidBcryptCost is returned when bcrypt cost is outside the accepted [10,31] range.
	ErrInvalidBcryptCost = errors.New("bcrypt cost must be between 10 and 31")
	// ErrInvalidPasswordParameter is returned by ValidatePasswordParameter when a
	// single auth.password.* value is out of range or the wrong type.
	ErrInvalidPasswordParameter = errors.New("invalid password hashing parameter")
	// ErrInvalidLaneThresholds is returned when the lane hysteresis band is
	// inverted or degenerate: lane_fast_threshold_ms must be strictly below
	// lane_slow_threshold_ms (and neither may be negative) so the dead-band
	// exists and a check cannot satisfy both edges at once.
	ErrInvalidLaneThresholds = errors.New(
		"scheduling lane thresholds must satisfy 0 <= lane_fast_threshold_ms < lane_slow_threshold_ms",
	)
	// ErrInvalidCNAMEMode is returned when server.custom_domain_cname_mode is
	// not one of "shared" / "token". Failing fast matters: silently falling back
	// to "shared" would drop the token-mode takeover protection.
	ErrInvalidCNAMEMode = errors.New("custom domain CNAME mode must be 'shared' or 'token'")
	// ErrACMEEmailRequired is returned when acme.enabled is true without an
	// account contact address — Let's Encrypt requires one for expiry notices.
	ErrACMEEmailRequired = errors.New("acme.email is required when acme.enabled is true")
	// ErrACMEListenRequired is returned when ACME is enabled but one of its
	// listen addresses was blanked out; both listeners are mandatory (HTTP-01
	// needs :80, TLS-ALPN-01 and serving need :443).
	ErrACMEListenRequired = errors.New("acme.listen_http and acme.listen_https are required when acme.enabled is true")
	// ErrACMEProxyProtocolCIDRsRequired is returned when acme.proxy_protocol is
	// switched on with an empty acme.proxy_protocol_trusted_cidrs list. The
	// PROXY header is an unauthenticated preamble, so trusting every source
	// would let anyone who can open a TCP connection forge a client IP into the
	// rate limiter — fail closed instead of defaulting to trust-everyone.
	ErrACMEProxyProtocolCIDRsRequired = errors.New(
		"acme.proxy_protocol_trusted_cidrs must list at least one CIDR when acme.proxy_protocol is true",
	)
	// ErrACMEProxyProtocolCIDRInvalid is returned when an entry of
	// acme.proxy_protocol_trusted_cidrs is neither a CIDR range ("10.0.0.0/8")
	// nor a bare IP address ("10.0.0.10").
	ErrACMEProxyProtocolCIDRInvalid = errors.New(
		"acme.proxy_protocol_trusted_cidrs entries must be a CIDR range or an IP address",
	)
	// ErrACMEFallbackUpstreamInvalid is returned when a fallback upstream is not
	// a dialable "host:port". A typo'd next hop would otherwise only surface as
	// every unknown-host connection being dropped, long after startup.
	ErrACMEFallbackUpstreamInvalid = errors.New(
		"acme.fallback_upstream_http / acme.fallback_upstream_https must be a 'host:port' address",
	)
	// ErrACMEFallbackUpstreamRequiresACME is returned when a fallback upstream is
	// configured with acme.enabled false: the two ACME listeners are the only
	// ones that forward, so without them the setting is a silent no-op.
	ErrACMEFallbackUpstreamRequiresACME = errors.New(
		"acme.fallback_upstream_http / acme.fallback_upstream_https require acme.enabled to be true",
	)
)

// Supported password-hashing algorithm identifiers.
const (
	PasswordAlgorithmArgon2id = "argon2id"
	PasswordAlgorithmBcrypt   = "bcrypt"
)

// Hard floors / OWASP advisory thresholds for password-hashing validation.
const (
	argon2MemoryFloorKiB = 8192  // hard reject below this (KiB)
	argon2MemoryOWASPKiB = 19456 // warn below this OWASP floor (KiB)
	argon2TimeFloor      = 1
	argon2ThreadsFloor   = 1
	argon2ThreadsMax     = 255 // uint8 ceiling, matches Argon2Params.Threads
	argon2KeyLengthFloor = 16
	argon2SaltLenFloor   = 8
	bcryptCostMin        = 10
	bcryptCostMax        = 31
	bcryptCostAdvisory   = 12 // warn below this
)

// ValidNodeRoles returns every role value SP_NODE_ROLE accepts on its own. A
// comma-separated SP_NODE_ROLE may only combine the MultiValueNodeRoles subset;
// see ParseNodeRoles in node_role.go.
func ValidNodeRoles() []string {
	return []string{NodeRoleAll, NodeRoleAPI, NodeRoleJobs, NodeRoleChecks, NodeRoleAgent}
}

// AgentConfig configures deported-agent mode (SP_NODE_ROLE=agent).
type AgentConfig struct {
	// ServerURL is the master's base URL (http(s)://host[:port]) —
	// SP_AGENT_SERVER_URL. The agent dials
	// <ServerURL>/api/v1/agent/ws (ws(s) scheme derived automatically).
	ServerURL string `koanf:"server_url"`
	// EnrollmentToken is the one-shot spe_ token used on the very first
	// connection — SP_AGENT_ENROLLMENT_TOKEN. Ignored once the agent has
	// enrolled (its persisted identity takes over).
	EnrollmentToken string `koanf:"enrollment_token"`
	// KeysFile is where the agent persists its identity/keys JSON —
	// SP_AGENT_KEYS_FILE (default /data/agent-keys.json; falls back to
	// ./agent-keys.json when /data is not writable).
	KeysFile string `koanf:"keys_file"`
	// Keys is the base64 of the identity/keys JSON for env-only deployments
	// (k8s secrets) — SP_AGENT_KEYS. Takes precedence over KeysFile. The koanf
	// tag deliberately does NOT claim "keys": the env loader maps
	// SP_AGENT_KEYS_FILE to agent.keys.file, which would collide with a string
	// at agent.keys and fail the unmarshal; both variables are read manually in
	// applyAgentEnv instead.
	Keys string `koanf:"keys_b64"`
	// Name is the agent display name sent at enrollment — SP_AGENT_NAME
	// (default: hostname).
	Name string `koanf:"name"`
	// PrintKeys opts into printing the base64 identity (private key material!)
	// to stdout — SP_AGENT_PRINT_KEYS. Off by default: the agent never emits
	// its own private keys unless an operator explicitly asks for them, because
	// stdout is aggregated by fly/Kubernetes/Docker/journald log drains. Use it
	// only to bootstrap an env-only (SP_AGENT_KEYS) deployment that has no
	// writable volume to read the keys file from, then unset it.
	PrintKeys bool `koanf:"print_keys"`
}

// OTelConfig contains OpenTelemetry configuration.
type OTelConfig struct {
	Enabled  bool   `koanf:"enabled"`
	Endpoint string `koanf:"endpoint"`
	Protocol string `koanf:"protocol"`
	Insecure bool   `koanf:"insecure"`
	Logs     bool   `koanf:"logs"`
	Traces   bool   `koanf:"traces"`
	Metrics  bool   `koanf:"metrics"`
}

// EncryptionConfig configures the credential-encryption KEK. The master
// key MUST come from outside the database (env var or mounted file). When
// neither is set the credentials service operates in disabled mode and
// secrets are stored in plaintext (V1 fallback for self-hosted users).
type EncryptionConfig struct {
	// MasterKey is the base64-encoded 32-byte KEK. SP_ENCRYPTION_MASTER_KEY.
	MasterKey string `koanf:"master_key"`
	// MasterKeyFile is the path to a file containing the base64 KEK.
	// SP_ENCRYPTION_MASTER_KEY_FILE. Wins over MasterKey when both set —
	// matches the Kubernetes secret-mount pattern.
	MasterKeyFile string `koanf:"master_key_file"`
	// AutoMigrate runs the encrypt-credentials sweep at startup when a
	// master key is configured. Defaults to true; set
	// SP_ENCRYPTION_AUTO_MIGRATE=false to opt out and run the CLI command
	// manually instead. Idempotent either way — only rows still in
	// plaintext are touched.
	AutoMigrate bool `koanf:"auto_migrate"`
}

// PrometheusConfig contains Prometheus metrics endpoint configuration.
type PrometheusConfig struct {
	Enabled bool   `koanf:"enabled"` // Enable the /metrics endpoint
	Path    string `koanf:"path"`    // Path for the metrics endpoint (default: /metrics)
}

// RealtimeConfig controls the org-scoped live hint WebSocket
// (GET /api/v1/orgs/:org/events/ws). When disabled the endpoint still
// upgrades but immediately closes with 4404 (same "feature disabled"
// convention as SP_PROMETHEUS_ENABLED's 404) and the dashboard silently keeps
// polling.
type RealtimeConfig struct {
	// Enabled gates the WebSocket endpoint and the hint publisher/hub.
	Enabled bool `koanf:"enabled"`
	// FlushInterval is the hint coalescing window per org per instance
	// (SP_REALTIME_FLUSH_INTERVAL, default 1s).
	FlushInterval time.Duration `koanf:"flush_interval"`
	// PingInterval is the transport-level ping keep-alive period
	// (SP_REALTIME_PING_INTERVAL, default 25s).
	PingInterval time.Duration `koanf:"ping_interval"`
	// MaxConnections caps concurrent hint connections per instance
	// (SP_REALTIME_MAX_CONNECTIONS, default 1000; 0 = unlimited).
	MaxConnections int `koanf:"max_connections"`
	// MaxSubscriptionsPerConnection caps how many scopes a single connection
	// may subscribe to (SP_REALTIME_MAX_SUBSCRIPTIONS_PER_CONNECTION, default
	// 512; 0 = unlimited).
	MaxSubscriptionsPerConnection int `koanf:"max_subscriptions_per_connection"`
}

// CheckersConfig controls which check types are enabled at the server level.
type CheckersConfig struct {
	Enabled       []string `koanf:"enabled"`        // Explicit allowlist (empty = all)
	Disabled      []string `koanf:"disabled"`       // Blocklist (applied after labels)
	EnabledLabels []string `koanf:"enabled_labels"` // Enable types matching any of these labels
	// Browser configures the Chrome backend the `browser` check type runs on.
	Browser BrowserCheckerConfig `koanf:"browser"`
}

// BrowserCheckerConfig selects where the `browser` check type finds Chrome.
//
// BOTH KEYS ARE SNAKE_CASE AND THEREFORE UNREACHABLE BY koanf's ENV LOADER —
// SP_CHECKERS_BROWSER_CDP_URL would land on checkers.browser.cdp.url, not
// cdp_url. They are bound by hand in applyCheckersEnv, the same quirk as
// rate_limiting / shutdown_timeout, and listed in manualReaderEnvVars so the
// startup env check does not flag them as typos.
type BrowserCheckerConfig struct {
	// CDPURL is the websocket (or http) address of a long-lived headless Chrome
	// speaking the Chrome DevTools Protocol — e.g. `ws://browser:9222` or
	// `http://127.0.0.1:9222`, typically a `chromedp/headless-shell` sidecar.
	// When set it is the primary path: no local binary is needed and no Chrome
	// process is cold-started per execution.
	CDPURL string `koanf:"cdp_url"`
	// ChromePath is the local Chrome/Chromium binary used by the exec fallback
	// when CDPURL is empty. Empty means "probe the usual names", which is
	// today's zero-config dev-laptop behavior. Nothing is ever downloaded.
	ChromePath string `koanf:"chrome_path"`
}

// SentryConfig contains Sentry error tracking configuration.
type SentryConfig struct {
	DSN              string  `koanf:"dsn"`                // Sentry DSN (empty = disabled)
	Environment      string  `koanf:"environment"`        // development, staging, production
	TracesSampleRate float64 `koanf:"traces_sample_rate"` // 0.0 to 1.0 (default 0.0 — see defaults block)
	Debug            bool    `koanf:"debug"`              // Enable Sentry debug logging
}

// PostHogConfig holds the product-analytics (PostHog) settings.
//
// The integration is *entirely inert* unless an operator configures it: with no
// ProjectAPIKey nothing is instantiated server-side, nothing is advertised to
// the browser through GET /api/v1/config, and the dashboard never loads any
// analytics code. Enabled is a kill switch that does NOT enable anything on its
// own — see Active, which is the single enablement rule shared verbatim by the
// backend, the public config endpoint and the dashboard.
type PostHogConfig struct {
	// Enabled is the kill switch (SP_POSTHOG_ENABLED, default true). It only
	// ever turns the feature *off*: a key is still required to turn it on.
	Enabled bool `koanf:"enabled"`
	// Host is the PostHog ingestion endpoint (SP_POSTHOG_HOST). When empty the
	// dashboard captures through the first-party PostHogProxyPath (so ad
	// blockers do not drop events) while the server-side client and that proxy
	// talk to DefaultPostHogHost. Set it to point at self-hosted PostHog or an
	// external reverse proxy, in which case the dashboard uses it verbatim.
	Host string `koanf:"host"`
	// ProjectAPIKey is the `phc_…` client key (SP_POSTHOG_PROJECT_API_KEY).
	// Public by design — it is shipped to the browser. Empty = feature off.
	ProjectAPIKey string `koanf:"project_api_key"`
	// PersonalAPIKey is an optional server-side key
	// (SP_POSTHOG_PERSONAL_API_KEY). SECRET: it is never returned by any API
	// and never reaches the browser. When empty the backend captures with the
	// project key.
	PersonalAPIKey string `koanf:"personal_api_key"`
}

// DefaultPostHogHost is the upstream ingestion endpoint used when none is
// configured. The server-side client and the built-in reverse proxy send here.
const DefaultPostHogHost = "https://eu.i.posthog.com"

// DefaultPostHogAssetsHost is where posthog-js fetches its static assets
// (toolbar, surveys, recorder). PostHog Cloud serves these from a separate
// host, so the reverse proxy forwards /static/ requests here.
const DefaultPostHogAssetsHost = "https://eu-assets.i.posthog.com"

// DefaultPostHogUIHost is the PostHog application host. The dashboard passes it
// as posthog-js ui_host so the toolbar and "view in PostHog" links resolve to
// the app even when api_host is the first-party PostHogProxyPath.
const DefaultPostHogUIHost = "https://eu.posthog.com"

// PostHogProxyPath is the first-party path the SolidPing origin reverse-proxies
// to PostHog (see internal/app/server.go). The dashboard uses it as api_host
// when no host is configured, so ad blockers that block third-party analytics
// hosts do not silently drop events.
const PostHogProxyPath = "/ingest"

// Active reports whether PostHog is on. This is THE enablement rule, applied
// identically by the backend analytics client, the public config endpoint and
// the dashboard: `enabled == true && project_api_key != ""`. Anything else is
// off — in particular a key with Enabled=false, and Enabled=true with no key.
func (c PostHogConfig) Active() bool {
	return c.Enabled && strings.TrimSpace(c.ProjectAPIKey) != ""
}

// ResolvedHost returns the upstream ingestion host, falling back to the default
// when the operator left it empty. This is the host the server-side client and
// the reverse proxy talk to — always an absolute URL, never the proxy path.
func (c PostHogConfig) ResolvedHost() string {
	if h := strings.TrimSpace(c.Host); h != "" {
		return h
	}

	return DefaultPostHogHost
}

// BrowserAPIHost returns the api_host the dashboard posts events to. With no
// explicit host it is the first-party PostHogProxyPath, so ingestion travels
// through the SolidPing origin; an operator-configured host is used verbatim.
func (c PostHogConfig) BrowserAPIHost() string {
	if h := strings.TrimSpace(c.Host); h != "" {
		return h
	}

	return PostHogProxyPath
}

// BrowserUIHost returns the ui_host the dashboard passes to posthog-js, or ""
// when no override is needed. It is set only when api_host is the first-party
// proxy path: posthog-js would otherwise derive the UI host from api_host and
// resolve the toolbar and "view in PostHog" links to a local path. With an
// operator-configured host, posthog-js derives the UI host as before.
func (c PostHogConfig) BrowserUIHost() string {
	if strings.TrimSpace(c.Host) != "" {
		return ""
	}

	return DefaultPostHogUIHost
}

// WebPushConfig holds VAPID credentials for Web Push notifications.
// Keys are auto-generated at first startup when not pre-provisioned.
type WebPushConfig struct {
	VAPIDPublicKey  string `koanf:"vapid_public_key"`
	VAPIDPrivateKey string `koanf:"vapid_private_key"`
	Subject         string `koanf:"subject"` // e.g. "mailto:admin@example.com"
	Enabled         bool   `koanf:"enabled"`
}

// Config represents the application configuration structure.
type Config struct {
	Server       ServerConfig         `koanf:"server"`
	Database     DatabaseConfig       `koanf:"db"`
	Auth         AuthConfig           `koanf:"auth"`
	Encryption   EncryptionConfig     `koanf:"encryption"`
	Email        EmailConfig          `koanf:"email"`
	Slack        SlackConfig          `koanf:"slack"`
	MSTeams      MSTeamsConfig        `koanf:"msteams"`
	WhatsApp     WhatsAppConfig       `koanf:"whatsapp"`
	Telegram     TelegramConfig       `koanf:"telegram"`
	SMS          SMSConfig            `koanf:"sms"`
	Voice        VoiceConfig          `koanf:"voice"`
	Google       GoogleOAuthConfig    `koanf:"google"`
	GitHub       GitHubOAuthConfig    `koanf:"github"`
	Microsoft    MicrosoftOAuthConfig `koanf:"microsoft"`
	GitLab       GitLabOAuthConfig    `koanf:"gitlab"`
	Discord      DiscordOAuthConfig   `koanf:"discord"`
	OIDC         OIDCOAuthConfig      `koanf:"oidc"`
	SAML         SAMLConfig           `koanf:"saml"`
	LDAP         LDAPConfig           `koanf:"ldap"`
	Node         NodeConfig           `koanf:"node"`
	Agent        AgentConfig          `koanf:"agent"`
	Profiler     ProfilerConfig       `koanf:"profiler"`
	Runtime      RuntimeConfig        `koanf:"runtime"`
	OTel         OTelConfig           `koanf:"otel"`
	Sentry       SentryConfig         `koanf:"sentry"`
	Prometheus   PrometheusConfig     `koanf:"prometheus"`
	Realtime     RealtimeConfig       `koanf:"realtime"`
	Checkers     CheckersConfig       `koanf:"checkers"`
	Aggregation  AggregationConfig    `koanf:"aggregation"`
	Jobs         JobsConfig           `koanf:"jobs"`
	FileStorage  FileStorageConfig    `koanf:"filestorage"`
	App          AppConfig            `koanf:"app"`
	Deployment   DeploymentConfig     `koanf:"deployment"`
	WebPush      WebPushConfig        `koanf:"webpush"`
	PostHog      PostHogConfig        `koanf:"posthog"`
	Entitlements EntitlementsConfig   `koanf:"entitlements"`
	ACME         ACMEConfig           `koanf:"acme"`
	RunMode      string               `koanf:"runmode"`   // "test" for test mode, empty for normal mode
	UserAgent    string               `koanf:"useragent"` // Identity string for protocol checks (SP_USERAGENT)
	LogLevel     slog.Level           `koanf:"-"`         // Logging level (parsed from LOG_LEVEL env var)
}

// ACMEConfig turns on in-server TLS: certmagic obtains and renews Let's Encrypt
// certificates on demand for the instance's own hosts and for verified custom
// domains, storing them in the database (tls_storage) so a cluster shares them
// and a restart never re-issues. Off by default — with acme.enabled false the
// server behaves exactly as before and TLS stays with an external proxy (the
// GET /api/v1/public/custom-domains/allowed contract is unaffected either way).
//
// Every key except enabled/email contains an underscore, which koanf's env
// loader would turn into a dot (acme.ca.url), so those are read by hand in
// applyACMEEnv. See project_koanf_env_quirk.
type ACMEConfig struct {
	// Enabled is the master switch (SP_ACME_ENABLED). false = zero behavior
	// change: no extra listeners, no CA traffic, no storage writes.
	Enabled bool `koanf:"enabled"`
	// Email is the ACME account contact (SP_ACME_EMAIL). Required when enabled.
	Email string `koanf:"email"`
	// CAURL is the ACME directory URL (SP_ACME_CA_URL). Empty uses certmagic's
	// default (Let's Encrypt production); override for LE staging or a Pebble
	// test CA.
	CAURL string `koanf:"ca_url"`
	// ListenHTTP is the challenge + redirect listener (SP_ACME_LISTEN_HTTP,
	// default ":80"). It serves /.well-known/acme-challenge/ and 308-redirects
	// everything else to https.
	ListenHTTP string `koanf:"listen_http"`
	// ListenHTTPS is the TLS listener (SP_ACME_LISTEN_HTTPS, default ":443").
	// Requests flow into the same handler chain as the plain listener, so
	// custom-host routing applies unchanged.
	ListenHTTPS string `koanf:"listen_https"`
	// ProxyProtocol makes both ACME listeners read a PROXY protocol (v1/v2)
	// preamble before the payload (SP_ACME_PROXY_PROTOCOL, default false).
	// Needed behind a TLS passthrough (a Traefik `HostSNI(`*`)` TCP router, for
	// instance): the proxy never sees HTTP, so there is no X-Forwarded-For and
	// every request would otherwise appear to come from the proxy's own IP —
	// collapsing per-IP rate limiting and abuse logging.
	ProxyProtocol bool `koanf:"proxy_protocol"`
	// ProxyProtocolTrustedCIDRs lists the sources whose PROXY header is honored
	// (SP_ACME_PROXY_PROTOCOL_TRUSTED_CIDRS, comma-separated). Entries are CIDR
	// ranges or bare IPs. A connection from a trusted source may or may not send
	// a header (health probes do not); one from anywhere else keeps its real
	// peer address no matter what preamble it sends. Required — and validated
	// non-empty — when ProxyProtocol is true.
	ProxyProtocolTrustedCIDRs []string `koanf:"proxy_protocol_trusted_cidrs"`
	// FallbackUpstreamHTTPS is the next hop for TLS connections whose SNI this
	// instance does not serve (SP_ACME_FALLBACK_UPSTREAM_HTTPS, "host:port").
	// Empty (the default) = no forwarding: an unknown SNI is refused here
	// exactly as before. Set it to chain a second instance behind the same
	// single-catch-all edge — this instance keeps its own domains and hands
	// everything else on, unterminated, with a PROXY v2 header carrying the
	// original client.
	FallbackUpstreamHTTPS string `koanf:"fallback_upstream_https"`
	// FallbackUpstreamHTTP is the same next hop for the plaintext :80 listener
	// (SP_ACME_FALLBACK_UPSTREAM_HTTP). Without it the downstream instance can
	// never solve an HTTP-01 challenge for its own domains, so it is normally
	// set together with FallbackUpstreamHTTPS.
	FallbackUpstreamHTTP string `koanf:"fallback_upstream_http"`
	// FallbackUpstreamProxyProtocol prefixes every forwarded connection with a
	// PROXY protocol v2 header (SP_ACME_FALLBACK_UPSTREAM_PROXY_PROTOCOL,
	// default true). Without it the downstream sees this instance's address as
	// the client on every forwarded connection. Turn it off only when the next
	// hop cannot parse a PROXY preamble.
	FallbackUpstreamProxyProtocol bool `koanf:"fallback_upstream_proxy_protocol"`
}

// Default listen addresses for in-server ACME. Remap them when the process
// cannot bind privileged ports and something forwards to it instead.
const (
	// DefaultACMEListenHTTP is where the HTTP-01 challenge + redirect listener
	// binds; a CA always starts HTTP-01 validation on port 80.
	DefaultACMEListenHTTP = ":80"
	// DefaultACMEListenHTTPS is where the TLS listener binds; a CA always starts
	// TLS-ALPN-01 validation on port 443.
	DefaultACMEListenHTTPS = ":443"
	// maxTCPPort bounds the port of a fallback upstream address.
	maxTCPPort = 65535
)

// DeploymentConfig picks per-org entitlement defaults. SP_DEPLOYMENT_MODE
// drives Mode; "self-hosted" (default) caps SSO membership at 30,
// "saas" caps aggregate check executions at 6/min. Validation is at
// startup — unknown values fail fast.
type DeploymentConfig struct {
	Mode string `koanf:"mode"`
}

// EntitlementsConfig tunes the per-org SMS/voice runaway guard — an in-memory
// hourly token bucket that bounds a broken escalation loop independent of the
// billing-driven monthly quota. Because these keys contain underscores that the
// env TransformFunc turns into dots, they are read manually from
// SP_ENTITLEMENTS_SMS_RUNAWAY_PER_HOUR / SP_ENTITLEMENTS_CALL_RUNAWAY_PER_HOUR.
type EntitlementsConfig struct {
	// SMSRunawayPerHour caps outbound SMS per org per hour (default 30).
	SMSRunawayPerHour int `koanf:"sms_runaway_per_hour"`
	// CallRunawayPerHour caps outbound voice calls per org per hour (default 10).
	CallRunawayPerHour int `koanf:"call_runaway_per_hour"`
	// WhatsAppRunawayPerHour caps outbound WhatsApp template messages per org
	// per hour (default 30). Unlike SMS this is instance-billed even for
	// self-hosters, so the guard matters just as much.
	WhatsAppRunawayPerHour int `koanf:"whatsapp_runaway_per_hour"`
	// TelegramRunawayPerHour caps outbound Telegram messages per org per hour
	// (default 60). Telegram messages are free, so there is deliberately NO
	// monthly entitlement for the channel — this guard exists purely to bound a
	// flapping check or a dispatch loop, hence the higher default.
	TelegramRunawayPerHour int `koanf:"telegram_runaway_per_hour"`
}

// NodeConfig contains node role configuration.
type NodeConfig struct {
	// Role is the raw SP_NODE_ROLE value: one of all, api, jobs, checks, agent,
	// or a comma-separated combination of api/jobs/checks (e.g. "api,jobs" for a
	// node that serves the dashboard and processes jobs while dedicated
	// checks-only nodes execute the checks). Parse it with Config.NodeRoles()
	// rather than comparing the string.
	Role   string `koanf:"role"`
	Region string `koanf:"region"` // Node region (required when the role set contains checks)
	// Name overrides the worker slug/name this process registers under
	// (SP_NODE_NAME). Empty means "derive it from os.Hostname()", which is the
	// historic behavior. Set it wherever the hostname is not stable, not
	// unique within the first 15 characters, or not slug-legal — most notably
	// Kubernetes pods running with `hostNetwork: true`, where the host UTS
	// namespace makes os.Hostname() return the (dotted) node name.
	Name string `koanf:"name"`
}

// ProfilerConfig contains pprof profiler server configuration.
type ProfilerConfig struct {
	Enabled bool   `koanf:"enabled"` // Enable the profiler server
	Listen  string `koanf:"listen"`  // Listen address (e.g., "localhost:6060")
	// BlockRate is passed to runtime.SetBlockProfileRate when > 0, enabling
	// /debug/pprof/block. 1 = sample every blocking event (highest fidelity,
	// highest cost); larger N samples 1/N. 0 (default) disables block profiling.
	BlockRate int `koanf:"block_rate"`
	// MutexFraction is passed to runtime.SetMutexProfileFraction when > 0,
	// enabling /debug/pprof/mutex. 1 = report every mutex contention event;
	// larger N reports 1/N. 0 (default) disables mutex profiling.
	MutexFraction int `koanf:"mutex_fraction"`
}

// RuntimeConfig contains Go runtime memory guardrails (GOMEMLIMIT soft cap and
// GOGC). Applied once at startup by internal/memlimit. A native GOMEMLIMIT /
// GOGC env var always overrides these knobs.
type RuntimeConfig struct {
	// MemoryLimit is the explicit GOMEMLIMIT soft cap (SP_RUNTIME_MEMORY_LIMIT).
	// Accepts human sizes ("400MiB", "1GiB") or raw bytes; empty = unset.
	MemoryLimit string `koanf:"memory_limit"`
	// AutoMemoryLimit derives the soft cap from the container's cgroup memory
	// limit when MemoryLimit is unset and no GOMEMLIMIT env var is present
	// (SP_RUNTIME_AUTO_MEMORY_LIMIT). Default true: a no-op off-container.
	AutoMemoryLimit bool `koanf:"auto_memory_limit"`
	// MemoryLimitRatio is the fraction of the detected cgroup limit to use as
	// the soft cap (SP_RUNTIME_MEMORY_LIMIT_RATIO). Default 0.9.
	MemoryLimitRatio float64 `koanf:"memory_limit_ratio"`
	// GCPercent maps to GOGC / debug.SetGCPercent when > 0
	// (SP_RUNTIME_GC_PERCENT). 0 leaves the runtime default untouched.
	GCPercent int `koanf:"gc_percent"`
}

// CustomDomainCNAMETarget resolves the hostname customers point their
// status-page CNAME at. It prefers the explicit Server.CustomDomainCNAMETarget
// and otherwise derives it from the host of Server.BaseURL (port stripped).
// Returns "" when neither is set — the resolver treats that as
// "custom domains disabled".
func (c *Config) CustomDomainCNAMETarget() string {
	if t := strings.TrimSpace(c.Server.CustomDomainCNAMETarget); t != "" {
		return strings.ToLower(strings.TrimSuffix(t, "."))
	}

	if parsed, err := url.Parse(c.Server.BaseURL); err == nil {
		if host := parsed.Hostname(); host != "" {
			return strings.ToLower(host)
		}
	}

	return ""
}

// CustomDomainCNAMEMode resolves the configured CNAME verification mode,
// falling back to domainverify.ModeShared for an empty or unrecognized value
// (Validate rejects unrecognized values at startup, so a bad value never
// reaches here in a live server).
func (c *Config) CustomDomainCNAMEMode() domainverify.Mode {
	mode, _ := domainverify.ParseMode(c.Server.CustomDomainCNAMEMode)

	return mode
}

// EmailConfig contains SMTP email configuration.
type EmailConfig struct {
	Host               string `koanf:"host"`               // SMTP server hostname
	Port               int    `koanf:"port"`               // SMTP port (typically 587 for STARTTLS)
	Username           string `koanf:"username"`           // SMTP username
	Password           string `koanf:"password"`           // SMTP password
	From               string `koanf:"from"`               // Default sender address
	FromName           string `koanf:"fromname"`           // Display name for sender
	Enabled            bool   `koanf:"enabled"`            // Enable/disable email sending
	InsecureSkipVerify bool   `koanf:"insecureskipverify"` // Skip TLS certificate verification
	AuthType           string `koanf:"authtype"`           // SMTP auth type: plain, login, cram-md5 (default: login)
	Protocol           string `koanf:"protocol"`           // SMTP encryption: none, starttls, ssl (default: starttls)
}

// FileStorageConfig controls where File blobs are persisted. The bytes live
// behind one of the registered backends (local FS, S3); the metadata always
// lives in the `files` table. By default S3 credentials come from the standard
// AWS SDK chain (env, IAM role, shared config); set S3AccessKey/S3SecretKey to
// pin static credentials for self-hosted S3-compatible stores. The multi-word
// keys here are settable via env through applyFileStorageEnv (koanf's env
// loader collapses underscores and can't reach the snake_case koanf tags).
type FileStorageConfig struct {
	Type           string `koanf:"type"`              // "local" (default) or "s3"
	LocalRoot      string `koanf:"local_root"`        // local backend root, e.g. "./data/files"
	S3Bucket       string `koanf:"s3_bucket"`         // S3 backend bucket name
	S3Region       string `koanf:"s3_region"`         // S3 backend region
	S3Prefix       string `koanf:"s3_prefix"`         // optional key prefix
	S3Endpoint     string `koanf:"s3_endpoint"`       // custom endpoint, e.g. https://minio.local:9000
	S3UsePathStyle bool   `koanf:"s3_use_path_style"` // true for MinIO/Garage/Ceph
	S3AccessKey    string `koanf:"s3_access_key"`     // optional static cred (else AWS chain)
	S3SecretKey    string `koanf:"s3_secret_key"`     // optional static cred — never logged
}

// AppConfig contains application-level integration settings: in-app bug
// reports, feature flags computed at startup. Persisted state lives here
// (env / parameters), not in normal user-facing tables.
type AppConfig struct {
	// EnableBugReport is computed (App.GitHub.IssuesToken != "" && App.GitHub.Repo != "").
	// Never read directly from config — call ComputeBugReportEnabled.
	EnableBugReport bool            `koanf:"-"`
	GitHub          AppGitHubConfig `koanf:"github"`
}

// AppGitHubConfig holds the GitHub credentials used for in-app feature integrations
// (bug reports today). Token comes from env / system parameters; never from a user-facing API.
type AppGitHubConfig struct {
	IssuesToken string `koanf:"issues_token"` // fine-grained PAT, issues:write only
	Repo        string `koanf:"repo"`         // "owner/name"
}

// AggregationConfig controls how aggressively raw/hour/day result data is compacted.
// Each value is the number of completed periods of that tier to retain before
// rolling up to the next tier. Minimum 1 (the previous behavior).
//
// These koanf fields are the deprecated legacy fallback: the aggregation job and
// its read-side consumers resolve retention live from the
// performance.aggregation_retention_* system parameters (see
// jobtypes.retentionFromConfig), which is what the server "Aggregation" settings
// tab writes. Keep the defaults here in sync with jobtypes' default constants.
type AggregationConfig struct {
	RetentionRaw  int `koanf:"retention_raw"`  // hours of raw to keep (default 24)
	RetentionHour int `koanf:"retention_hour"` // days of hourly to keep (default 7)
	RetentionDay  int `koanf:"retention_day"`  // months of daily to keep (default 2)
}

// AuthConfig contains authentication configuration.
type AuthConfig struct {
	JWTSecret                string        `koanf:"jwt_secret"`
	AccessTokenExpiry        time.Duration `koanf:"access_token_expiry"`
	RefreshTokenExpiry       time.Duration `koanf:"refresh_token_expiry"`
	RegistrationEmailPattern string        `koanf:"registration_email_pattern"`
	// SessionMaxDuration is a hard absolute cap on session lifetime,
	// measured from login — unlike RefreshTokenExpiry (a sliding *idle*
	// window), this bounds total session length even under continuous
	// activity. Zero (the default) means unlimited: today's behavior, only
	// the sliding window applies. Overlaid from the
	// systemconfig.KeySessionMaxDuration system parameter at startup; an
	// org-scoped override of the same parameter key takes precedence over
	// this value (see handlers/auth.Service.resolveSessionMaxDuration).
	SessionMaxDuration time.Duration  `koanf:"session_max_duration"`
	WebAuthn           WebAuthnConfig `koanf:"webauthn"`
	Password           PasswordConfig `koanf:"password"`
}

// PasswordConfig selects the password-hashing algorithm and its cost
// parameters. Defaults reproduce the historical argon2id profile exactly, so
// upgrading the binary changes nothing until an operator reconfigures it. On a
// successful login, a stored hash whose algorithm or cost no longer matches this
// policy is transparently re-hashed (see internal/utils/passwords).
type PasswordConfig struct {
	Algorithm string       `koanf:"algorithm"` // "argon2id" (default) | "bcrypt"
	Argon2    Argon2Params `koanf:"argon2"`
	Bcrypt    BcryptParams `koanf:"bcrypt"`
	// RehashOnLogin gates the lazy login-time rehash. When true (default), a
	// stored hash that no longer matches the active policy is transparently
	// re-minted on the user's next successful password login. When false, only
	// new passwords (new users, password changes/resets) use the new profile;
	// existing hashes are left untouched.
	RehashOnLogin bool `koanf:"rehash_on_login"`
}

// Argon2Params are the argon2id cost parameters (memory in KiB).
type Argon2Params struct {
	Memory     uint32 `koanf:"memory"`      // KiB, default 65536 (64 MiB)
	Time       uint32 `koanf:"time"`        // default 3
	Threads    uint8  `koanf:"threads"`     // default 4
	KeyLength  uint32 `koanf:"key_length"`  // default 32
	SaltLength uint32 `koanf:"salt_length"` // default 16
}

// BcryptParams are the bcrypt cost parameters.
type BcryptParams struct {
	Cost int `koanf:"cost"` // default 12 (bcrypt range 4–31; validated >= 10)
}

// WebAuthnConfig configures the passkey / WebAuthn relying party. RPID
// must match the host the dashboard runs on (no scheme, no port). Origins
// are full origin strings (https://app.example.com). When RPID is empty
// it is derived from ServerConfig.BaseURL at startup; if the resolved
// scheme is not https (and host is not localhost) passkeys are disabled.
type WebAuthnConfig struct {
	Enabled       bool     `koanf:"enabled"`
	RPID          string   `koanf:"rp_id"`
	RPDisplayName string   `koanf:"rp_display_name"`
	Origins       []string `koanf:"origins"`
}

// SlackConfig contains Slack integration configuration.
type SlackConfig struct {
	Enabled          bool   `koanf:"enabled"`
	AppID            string `koanf:"app_id"`
	ClientID         string `koanf:"client_id"`
	ClientSecret     string `koanf:"client_secret"`
	SigningSecret    string `koanf:"signing_secret"`
	OAuthCallbackURL string `koanf:"oauth_callback_url"` // OAuth callback URL for user authentication
	// SocketModeEnabled toggles Slack Socket Mode (outgoing WebSocket) in
	// place of HTTPS webhook delivery. Mutually exclusive at the Slack App
	// configuration level — Slack delivers to exactly one transport.
	SocketModeEnabled bool   `koanf:"socket_mode_enabled"`
	AppToken          string `koanf:"app_token"` // xapp-... App-Level Token used for Socket Mode connection
}

// MSTeamsConfig contains the Microsoft Teams **bot** (Azure Bot / Bot
// Framework) integration configuration — the two-way `msteams-bot` connection
// type. It has nothing to do with the one-way `msteams` Workflow webhook,
// which needs no instance-level credential at all.
//
// Mirrors SlackConfig. Default Enabled:false — unlike Slack Socket Mode, Bot
// Framework has no outbound-dialing transport, so Microsoft must be able to
// reach this instance's messaging endpoint over public HTTPS. A self-hosted
// instance behind a firewall cannot use the bot, which is why it stays off
// until an operator explicitly turns it on.
//
// SaaS: one multi-tenant Entra app owned by SolidPing; TenantID stays empty so
// any installing tenant is accepted and the per-org connection is keyed by the
// tenant id captured at install. Self-hosted single-tenant: set TenantID to
// pin the allow-list to the operator's own tenant.
type MSTeamsConfig struct {
	Enabled   bool   `koanf:"enabled"`
	AppID     string `koanf:"app_id"`
	AppSecret string `koanf:"app_secret"`
	TenantID  string `koanf:"tenant_id"`
}

// WhatsApp defaults. Exported so the client package and tests share one source
// of truth for the pinned Graph API version and the template names.
const (
	// DefaultWhatsAppAPIVersion pins the Graph API version. Pinned rather than
	// floating so a Meta version rollout can never silently change payload
	// semantics under a running deployment; operators bump it deliberately via
	// SP_WHATSAPP_API_VERSION once they have re-tested.
	DefaultWhatsAppAPIVersion = "v23.0"
	// DefaultWhatsAppAlertTemplate is the Meta-approved *utility* template used
	// for incident alerts. One template covers down/escalate/resolve because the
	// new state is a body variable.
	DefaultWhatsAppAlertTemplate = "solidping_alert"
	// DefaultWhatsAppVerifyTemplate is the Meta-approved *authentication*
	// template used for the contact verification code.
	DefaultWhatsAppVerifyTemplate = "solidping_verify"
	// DefaultWhatsAppTemplateLanguage is the template language/locale code.
	DefaultWhatsAppTemplateLanguage = "en"
)

// WhatsAppConfig contains the instance-level WhatsApp Business Cloud API
// credentials. Mirrors SlackConfig: one deployment-wide identity, no per-org
// bring-your-own WABA in v1. SaaS supplies SP_WHATSAPP_* in its deployment env;
// a self-hoster does exactly the same with their own Meta app and WABA — no
// code path differs between the two.
//
// Default Enabled:false. AccessToken and AppSecret are SECRETS: env/SSM only,
// never logged, never returned by any API, never sent to a browser.
type WhatsAppConfig struct {
	// Enabled is the kill switch (SP_WHATSAPP_ENABLED). It only ever turns the
	// feature off — credentials are still required to turn it on (see Active).
	Enabled bool `koanf:"enabled"`
	// AccessToken is the permanent system-user token carrying the
	// whatsapp_business_messaging permission. SECRET.
	AccessToken string `koanf:"access_token"`
	// PhoneNumberID is the WABA phone-number id messages are sent from (the
	// numeric id, not the phone number itself).
	PhoneNumberID string `koanf:"phone_number_id"`
	// WABAID is the WhatsApp Business Account id. Not needed to send; kept for
	// operator diagnostics and future template-management calls.
	WABAID string `koanf:"waba_id"`
	// AppSecret signs inbound webhooks (X-Hub-Signature-256). SECRET.
	AppSecret string `koanf:"app_secret"`
	// WebhookVerifyToken is the shared string Meta echoes during the GET
	// webhook handshake. SECRET-ish: it is an authenticator, never logged.
	WebhookVerifyToken string `koanf:"webhook_verify_token"`
	// APIVersion is the pinned Graph API version segment (e.g. "v23.0").
	APIVersion string `koanf:"api_version"`
	// AlertTemplate / VerifyTemplate are the approved template names.
	AlertTemplate  string `koanf:"alert_template"`
	VerifyTemplate string `koanf:"verify_template"`
	// TemplateLanguage is the template language code both templates use.
	TemplateLanguage string `koanf:"template_language"`
	// BaseURL overrides the Graph API base (SP_WHATSAPP_BASE_URL). Empty means
	// the real graph.facebook.com. Exists so an operator can front Meta with an
	// egress proxy, and so test mode / E2E can point the whole feature at a
	// fake Graph API without any code path differing.
	BaseURL string `koanf:"base_url"`
}

// Active reports whether WhatsApp can actually send. This is THE enablement
// rule, applied identically by the sender, the escalation dispatcher, the
// verification flow and the public config endpoint: the kill switch must be on
// AND the two credentials a send cannot work without must be present. Anything
// else is off — in particular Enabled=true with no token.
func (c *WhatsAppConfig) Active() bool {
	return c.Enabled &&
		strings.TrimSpace(c.AccessToken) != "" &&
		strings.TrimSpace(c.PhoneNumberID) != ""
}

// ResolvedAPIVersion returns the configured Graph API version or the default.
func (c *WhatsAppConfig) ResolvedAPIVersion() string {
	if v := strings.TrimSpace(c.APIVersion); v != "" {
		return v
	}

	return DefaultWhatsAppAPIVersion
}

// ResolvedAlertTemplate returns the configured alert template name or the default.
func (c *WhatsAppConfig) ResolvedAlertTemplate() string {
	if v := strings.TrimSpace(c.AlertTemplate); v != "" {
		return v
	}

	return DefaultWhatsAppAlertTemplate
}

// ResolvedVerifyTemplate returns the configured verify template name or the default.
func (c *WhatsAppConfig) ResolvedVerifyTemplate() string {
	if v := strings.TrimSpace(c.VerifyTemplate); v != "" {
		return v
	}

	return DefaultWhatsAppVerifyTemplate
}

// ResolvedTemplateLanguage returns the configured template language or the default.
func (c *WhatsAppConfig) ResolvedTemplateLanguage() string {
	if v := strings.TrimSpace(c.TemplateLanguage); v != "" {
		return v
	}

	return DefaultWhatsAppTemplateLanguage
}

// DefaultTelegramBaseURL is the real Bot API base. Exported so the client
// package and its tests share one source of truth.
const DefaultTelegramBaseURL = "https://api.telegram.org"

// TelegramConfig contains the instance-level Telegram Bot API credentials.
// Mirrors WhatsAppConfig: one deployment-wide bot identity, no per-org
// bring-your-own bot in v1. A SaaS deployment supplies SP_TELEGRAM_* in its
// deployment env; a self-hoster does exactly the same with their own @BotFather
// bot — no code path differs between the two.
//
// A Telegram bot can hold only ONE webhook URL, so dev and prod each need their
// own bot and their own token. That is a platform constraint, not a convention.
//
// Default Enabled:false. BotToken and WebhookSecret are SECRETS: env/SSM only,
// never logged, never returned by any API, never sent to a browser.
type TelegramConfig struct {
	// Enabled is the TRI-STATE kill switch (SP_TELEGRAM_ENABLED):
	//
	//	nil   → auto: on iff a bot token is present
	//	false → off, whatever else is configured
	//	true  → explicitly on (still needs a token to do anything)
	//
	// A pointer because a bare bool cannot express "unset", and "unset" is
	// exactly what makes the bot token alone sufficient: supplying a token IS
	// the intent to enable, so demanding a second variable to confirm it was
	// pure ceremony. See IsEnabled.
	Enabled *bool `koanf:"enabled"`
	// BotToken is the @BotFather token (`123456789:AA…`). SECRET.
	BotToken string `koanf:"bot_token"`
	// BotUsername is the bot's @username without the leading '@' (e.g.
	// "solidping_bot"). Public by nature: the browser needs it to build the
	// t.me deep link, so it is part of the enablement rule rather than optional.
	BotUsername string `koanf:"bot_username"`
	// WebhookSecret is the shared string Telegram echoes in the
	// X-Telegram-Bot-Api-Secret-Token header. It is the ONLY authenticity gate
	// on the inbound webhook (Telegram does not sign payloads), so it must be
	// high-entropy (≥32 bytes). SECRET.
	WebhookSecret string `koanf:"webhook_secret"`
	// BaseURL overrides the Bot API base (SP_TELEGRAM_BASE_URL). Empty means the
	// real api.telegram.org. Exists so an operator can front Telegram with an
	// egress proxy, and so tests / test mode can point the whole feature at an
	// httptest fake without any code path differing.
	BaseURL string `koanf:"base_url"`
}

// BoolPtr returns a pointer to b, for the tri-state config switches where a
// nil pointer means "unset / auto" rather than false.
func BoolPtr(b bool) *bool {
	return &b
}

// IsEnabled collapses the tri-state switch to the effective on/off state:
// an explicit value wins, and unset means "on iff a bot token is present".
func (c *TelegramConfig) IsEnabled() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}

	return strings.TrimSpace(c.BotToken) != ""
}

// Configured reports whether the instance holds a usable bot identity — i.e.
// whether it can talk to the Bot API at all. The token IS the identity, so it
// is the only irreducible input.
//
// This is the gate for everything that does not need a connect link: the
// inbound webhook route, escalation dispatch, boot-time bootstrap and the
// client constructor. In particular the WEBHOOK ROUTE MUST NOT depend on the
// username: on a first boot holding only a token the username is not known yet,
// and a route that was never registered cannot be fixed by any later GetMe
// without a restart. Registering it early is harmless — the handler rejects
// everything failing the secret check, so the endpoint is 403-only until the
// rest is resolved.
func (c *TelegramConfig) Configured() bool {
	return c.IsEnabled() && strings.TrimSpace(c.BotToken) != ""
}

// Active reports whether a user can actually CONNECT a chat, which additionally
// requires the bot's @username to build the t.me deep link.
//
// Deliberately narrower than Configured: this gates the connect surface only —
// the public config flag and the connect-link endpoint. Conflating the two is
// what used to make SP_TELEGRAM_BOT_USERNAME mandatory even for sending.
func (c *TelegramConfig) Active() bool {
	return c.Configured() && strings.TrimSpace(c.BotUsername) != ""
}

// ResolvedBaseURL returns the configured Bot API base or the default.
func (c *TelegramConfig) ResolvedBaseURL() string {
	if v := strings.TrimSpace(c.BaseURL); v != "" {
		return strings.TrimRight(v, "/")
	}

	return DefaultTelegramBaseURL
}

// ResolvedBotUsername returns the bot username without a leading '@', which is
// what a t.me/<username> deep link needs. Operators paste it both ways.
func (c *TelegramConfig) ResolvedBotUsername() string {
	return strings.TrimPrefix(strings.TrimSpace(c.BotUsername), "@")
}

// JobWorkerConfig contains job worker configuration.
type JobWorkerConfig struct {
	FetchMaxAhead time.Duration `koanf:"fetch_max_ahead"` // Max time ahead to look for jobs
	Nb            int           `koanf:"nb"`              // Max concurrent goroutines
}

// JobsConfig controls the background-job stuck-job reaper. The reaper recovers
// jobs left in 'running' by a dead/redeployed worker by riding the existing
// retry chain (retried + backoff clone) until maxRetryCount, then 'failed'.
type JobsConfig struct {
	// StuckTimeout is how long a job may stay in 'running' without its
	// updated_at moving before the reaper treats it as orphaned. Must be
	// generously larger than the slowest legitimate job (no lease yet, so the
	// reaper cannot tell a dead worker from a slow one — see spec §3).
	StuckTimeout time.Duration `koanf:"stuck_timeout"`

	// ReaperInterval is how often the stuck-job reaper wakes up.
	ReaperInterval time.Duration `koanf:"reaper_interval"`
}

// CheckWorkerConfig contains check runner configuration.
type CheckWorkerConfig struct {
	FetchMaxAhead time.Duration `koanf:"fetch_max_ahead"` // Max time ahead to look for jobs
	Nb            int           `koanf:"nb"`              // Max concurrent goroutines
	Region        string        `koanf:"region"`          // Worker region (e.g., "us-east-1", "eu-west-1")
}

// RateLimitConfig controls the per-IP HTTP rate and concurrency limiters.
type RateLimitConfig struct {
	// RequestsPerMinute is the token-bucket refill rate per IP per minute.
	// 0 disables the rate limiter entirely.
	RequestsPerMinute int `koanf:"requests_per_minute"`

	// Burst is the maximum instantaneous burst above the sustained rate.
	// Defaults to RequestsPerMinute / 5 (one-fifth of a minute's allowance).
	Burst int `koanf:"burst"`

	// MaxConcurrent is the maximum number of in-flight requests per IP.
	// 0 disables the concurrency limiter entirely.
	MaxConcurrent int `koanf:"max_concurrent"`

	// TrustedProxies is the number of trusted reverse-proxy hops.
	// 0 means use RemoteAddr directly (safe default for direct deployments).
	// Set to 1 if behind a single nginx/ingress that sets X-Forwarded-For.
	TrustedProxies int `koanf:"trusted_proxies"`

	// TokenBucketsPerIP caps how many distinct bearer-token buckets a single
	// client IP may hold live at once. Authenticated requests are keyed by a
	// hash of their (unverified) bearer token so users behind a shared
	// NAT/VPN egress each get their own bucket; the cap keeps "mint a fresh
	// token, get a fresh bucket" bounded to at most this multiple of the
	// per-IP allowance. 0 disables token keying (all requests keyed by IP).
	TokenBucketsPerIP int `koanf:"token_buckets_per_ip"`

	// RateQueue is the per-IP waiting-room size for requests that lost the
	// fast-path token race. Up to this many requests may wait for the next
	// token refill before being rejected with 429. 0 disables the slow lane
	// (legacy behavior: 429 the moment the bucket is empty).
	RateQueue int `koanf:"rate_queue"`

	// ConcurrencyQueue is the per-IP waiting-room size for requests that
	// did not acquire a concurrency slot. Up to this many requests may wait
	// for an active slot to free before being rejected with 429. 0 disables
	// the waiting room.
	ConcurrencyQueue int `koanf:"concurrency_queue"`

	// MaxQueueWait is the hard ceiling on how long a request may sit in
	// either queue before being rejected with 429. 0 disables the ceiling
	// (only client cancellation ends the wait).
	MaxQueueWait time.Duration `koanf:"max_queue_wait"`
}

// DefaultRateLimitConfig returns the built-in per-client HTTP limit defaults.
//
// Sizing is anchored to real dash0 traffic, not abuse math: on an org with
// live checks, the checks page holds one query per check-group panel and
// refetches all of them on every realtime-hint tick (min 3s apart, see
// dash0's LIVE_INVALIDATE_MIN_INTERVAL_MS), so one tab sustains ~20
// requests/min per panel — ~500 req/min for a 25-group org — and a cold
// page load fires every panel query in parallel. The previous defaults
// (300/min, burst 60, 10-deep queues) put a single busy tab at ~65% of the
// whole budget, so a reload or second tab produced a steady trickle of
// 429s. These limits are a guard against runaway clients and cheap floods,
// not a fairness quota; they must sit well above any traffic the dashboard
// itself can generate.
func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{
		// ~3 busy tabs on a 25-group org (3 × 26 panels × 20/min ≈ 1560).
		RequestsPerMinute: 1800,
		// Several heavy cold loads back-to-back (RequestsPerMinute / 5).
		Burst:             360,
		MaxConcurrent:     20,
		TrustedProxies:    0,
		TokenBucketsPerIP: 50,
		// At the 30/s refill rate a full rate queue drains in 2s, so brief
		// overshoot degrades into a short delay instead of a 429.
		RateQueue: 60,
		// One cold load fires every panel query at once (browsers multiplex
		// ~100 streams over a single HTTP/2 connection), so the waiting room
		// must hold a full page load's overflow beyond MaxConcurrent.
		ConcurrencyQueue: 40,
		MaxQueueWait:     30 * time.Second,
	}
}

// ServerConfig contains HTTP server configuration.
type ServerConfig struct {
	Listen          string            `koanf:"listen"`
	BaseURL         string            `koanf:"base_url"`     // Public URL where SolidPing is accessible
	JobWorker       JobWorkerConfig   `koanf:"job_worker"`   // TODO: Move it to Config
	CheckWorker     CheckWorkerConfig `koanf:"check_worker"` // TODO: Move it to Config
	ShutdownTimeout time.Duration     `koanf:"shutdown_timeout"`
	// MaxRequestDuration is the total per-request budget covering rate-limit
	// queue wait, concurrency-queue wait, and handler execution. 0 disables
	// the timeout. Exceeded requests get a 504 REQUEST_TIMEOUT response.
	MaxRequestDuration time.Duration   `koanf:"max_request_duration"`
	RateLimiting       RateLimitConfig `koanf:"rate_limiting"` // Per-IP HTTP rate and concurrency limits
	Redirects          []RedirectRule  `koanf:"-"`             // Parsed from SP_REDIRECTS env var
	// DocsHost is the Host header that serves the embedded docs site (docsres)
	// at the host root, e.g. "docs.solidping.io". Empty disables host-served
	// docs. Multi-word koanf key → read via applyServerEnv (SP_DOCS_HOST /
	// SP_SERVER_DOCS_HOST), not the auto env loader.
	DocsHost string `koanf:"docs_host"`
	// CustomDomainCNAMETarget is the hostname customers point their status-page
	// CNAME at (e.g. "cname.solidping.io"). Empty derives it from the host of
	// BaseURL. Multi-word koanf key → read via applyServerEnv
	// (SP_CUSTOM_DOMAIN_CNAME_TARGET / SP_SERVER_CUSTOM_DOMAIN_CNAME_TARGET), not
	// the auto env loader. Resolve through Config.CustomDomainCNAMETarget().
	CustomDomainCNAMETarget string `koanf:"custom_domain_cname_target"`
	// CustomDomainCNAMEMode selects how a customer's single CNAME is verified:
	// "shared" (default) points at CustomDomainCNAMETarget directly, "token"
	// points at the page-specific "<token>.cname.<target>" host and therefore
	// survives a dangling-CNAME takeover attempt. Token mode requires a
	// wildcard A/AAAA (or ALIAS — never a CNAME) record for
	// "*.cname.<target>". Multi-word koanf key → read via applyServerEnv
	// (SP_CUSTOM_DOMAIN_CNAME_MODE / SP_SERVER_CUSTOM_DOMAIN_CNAME_MODE).
	// Resolve through Config.CustomDomainCNAMEMode().
	CustomDomainCNAMEMode string `koanf:"custom_domain_cname_mode"`
	// Scheduling holds the cost-aware, plan-weighted check-scheduling knobs.
	// Multi-word keys → read via applySchedulingEnv. See project_koanf_env_quirk.
	Scheduling SchedulingConfig `koanf:"scheduling"`
	// ExitWithParent makes the server shut down when the process that started
	// it disappears, instead of being reparented to PID 1 and outliving its
	// session (spec 2026-08-12-05). Off by default: a normal deployment is
	// started BY a supervisor whose death is not a reason to stop. Turn it on
	// for anything spawned by a test harness or an ad-hoc wrapper.
	// Multi-word koanf key → read via applyServerEnv (SP_EXIT_WITH_PARENT /
	// SP_SERVER_EXIT_WITH_PARENT), not the auto env loader.
	ExitWithParent bool `koanf:"exit_with_parent"`
}

// SchedulingConfig tunes cost-aware, plan-weighted check execution
// (spec 2026-06-30-09). The knobs only reorder fetch order (by adjusting a
// job's effective scheduled_at) and bound the per-check execution timeout —
// they never shed or defer a claimed job. The cost/delay deprioritization is
// driven by per-job EWMAs (0 until first run, so fresh jobs are pure FIFO); the
// tier credit and cost-aware timeout below are gated by their knobs (0 = off).
type SchedulingConfig struct {
	// SlowThresholdMs is the dead-band on the deprioritization offset: a check's
	// effective_scheduled_at is only pushed past its real scheduled_at once its
	// combined cost+delay EWMA reaches this many ms. Below it the offset is 0
	// (effective == scheduled_at), so small per-run variance never reorders fast
	// checks. Default 2000 (see Load); 0 disables the dead-band (any offset applies).
	SlowThresholdMs float64 `koanf:"slow_threshold_ms"`
	// CheckTimeoutMs is the global per-check execution ceiling in milliseconds.
	// It is the flat timeout when the cost-aware timeout is off, and the upper
	// clamp bound when it is on. The execution context is this + 1s so a checker
	// that honors its own timeout reports a clean StatusTimeout before the hard
	// context cancellation. Default 15000 (see Load); 0 falls back to the 15s
	// built-in default.
	CheckTimeoutMs float64 `koanf:"check_timeout_ms"`
	// TierCreditSeconds is the deadline credit per unit of plan_weight (paid
	// jobs sort earlier under contention). 0 disables the credit.
	TierCreditSeconds float64 `koanf:"tier_credit_seconds"`
	// TierCreditMaxSeconds caps total tier credit regardless of weight. 0 = no
	// separate cap.
	TierCreditMaxSeconds float64 `koanf:"tier_credit_max_seconds"`
	// CostTimeoutFactor multiplies cost_ewma_ms to derive the per-check
	// execution timeout, clamped to [floor, check_timeout_ms]. Default 3 (see
	// Load; on by default per spec 2026-07-01-04 D4). 0 disables (flat
	// check_timeout_ms). A job that never ran (cost 0) always keeps the full
	// ceiling regardless of the floor.
	CostTimeoutFactor float64 `koanf:"cost_timeout_factor"`
	// CostTimeoutFloorMs is the minimum cost-aware timeout in ms, so a fast
	// check is never given an unreasonably short ceiling. Default 5000 (see
	// Load). Only used when CostTimeoutFactor > 0.
	CostTimeoutFloorMs float64 `koanf:"cost_timeout_floor_ms"`

	// LaneSlowThresholdMs is the promote edge of the fast/slow lane hysteresis
	// band (spec 2026-07-01-03): a check whose cost EWMA reaches this many ms
	// is classified into the slow lane on its next post-exec write. Default
	// 2000 (see Load); 0 disables lane classification (jobs hold their stored
	// lane).
	LaneSlowThresholdMs float64 `koanf:"lane_slow_threshold_ms"`
	// LaneFastThresholdMs is the demote edge of the band: a slow-lane check
	// whose cost EWMA drops below this many ms returns to the fast lane.
	// Must be strictly below LaneSlowThresholdMs (validated); the dead-band
	// stops threshold hoverers from flipping lanes every run. Default 1000.
	LaneFastThresholdMs float64 `koanf:"lane_fast_threshold_ms"`
	// FastLaneReserved is the number of runner slots reserved for fast-lane
	// checks on each worker: slow jobs in flight never exceed pool_size −
	// FastLaneReserved, while fast jobs may use any free slot (slow borrows
	// idle fast capacity, never the reverse). Clamped to [0, pool_size−1]
	// with a startup warning when out of range. Default 5; 0 disables the
	// reservation (slow may fill the pool, pre-lane behavior).
	FastLaneReserved int `koanf:"fast_lane_reserved"`
}

// RedirectRule represents a path-based redirect configuration for development proxying.
type RedirectRule struct {
	PathPrefix string // e.g., "/dashboard"
	TargetHost string // e.g., "localhost:5173"
	TargetPath string // e.g., "/dashboard" or "/app"
}

// DatabaseConfig contains database connection configuration.
type DatabaseConfig struct {
	Type   string `koanf:"type"`   // "postgres", "postgres-embedded", "sqlite", or "sqlite-memory"
	URL    string `koanf:"url"`    // PostgreSQL DSN (for "postgres" type)
	Dir    string `koanf:"dir"`    // SQLite data directory (for "sqlite" type)
	LogSQL bool   `koanf:"logsql"` // Enable SQL query logging using slog
	Reset  bool   `koanf:"reset"`  // Reset database on startup (only for test/demo run modes)

	// MigrationGuardMode is "strict" (default, fail boot on checksum mismatch)
	// or "warn" (log and continue). Multi-word koanf key → read via
	// applyMigrationGuardModeEnv (SP_DB_MIGRATION_GUARD_MODE), not the auto env
	// loader. See project_koanf_env_quirk.
	MigrationGuardMode string `koanf:"migration_guard_mode"`

	// PostgreSQL connection-pool bounds. Without these, database/sql leaves the
	// pool unbounded (default MaxOpenConns = 0 = unlimited), so a burst can open
	// arbitrarily many connections — each with its own buffers client- and
	// server-side. SQLite ignores these (it is pinned to a single writer).
	MaxOpenConns    int           `koanf:"max_open_conns"`     // 0 = driver default (unlimited)
	MaxIdleConns    int           `koanf:"max_idle_conns"`     // 0 = driver default (2)
	ConnMaxLifetime time.Duration `koanf:"conn_max_lifetime"`  // 0 = no expiry
	ConnMaxIdleTime time.Duration `koanf:"conn_max_idle_time"` // 0 = no reap; idle conns held forever

	// SlowQueryThreshold logs a successful query at WARN once it takes at
	// least this long (internal/db/sloghook). 0 disables slow-query logging.
	// Multi-word koanf key → read via applyDBSlowQueryEnv
	// (SP_DB_SLOW_QUERY_THRESHOLD), not the auto env loader. See
	// project_koanf_env_quirk.
	SlowQueryThreshold time.Duration `koanf:"slow_query_threshold"`
}

// Load reads configuration from defaults, config file, and environment variables.
//
//nolint:funlen,cyclop // Configuration loading requires setting many defaults and has multiple branches
func Load() (*Config, error) {
	koanfInstance := koanf.New(".")

	// Set defaults
	defaults := Config{
		Server: ServerConfig{
			Listen:             ":4000",
			BaseURL:            "http://localhost:4000",
			DocsHost:           "docs.solidping.io",
			ShutdownTimeout:    30 * time.Second,
			MaxRequestDuration: 30 * time.Second,
			JobWorker: JobWorkerConfig{
				FetchMaxAhead: 5 * time.Minute,
				Nb:            2,
			},
			CheckWorker: CheckWorkerConfig{
				FetchMaxAhead: 5 * time.Minute,
				// Check execution is almost pure network I/O — a goroutine
				// parked on a slow socket costs ~KB and zero CPU — so a tiny
				// pool artificially manufactures head-of-line blocking (a
				// handful of slow checks = 100% occupancy = total stall). The
				// real bound is now DB-flush throughput, not this goroutine
				// count. Slow checks are deprioritized (not capped), so they
				// fall back in fetch order without being shed. See spec
				// 2026-06-30-09.
				Nb: 25,
			},
			Scheduling: SchedulingConfig{
				// Deprioritize a check only once its combined cost+delay EWMA
				// reaches this many ms; below it, effective_scheduled_at stays at
				// the real scheduled_at (pure FIFO) so small per-run variance never
				// reorders fast checks. Tier weighting stays opt-in (0).
				SlowThresholdMs: 2000,
				// Global per-check execution ceiling (spec 2026-07-10-11):
				// 15s by default. The execution context is this + 1s so a
				// checker's own timeout fires first and reports a clean
				// StatusTimeout. Override via config or
				// SP_SCHEDULING_CHECK_TIMEOUT_MS; 0 falls back to the 15s
				// built-in default.
				CheckTimeoutMs: 15000,
				// Cost-aware timeout is ON by default (spec 2026-07-01-04 D4):
				// timeout = clamp(3 × cost_ewma, 5s, check_timeout_ms). A 200ms check's
				// worst-case slot occupancy drops to the 5s floor while measured
				// slow checks keep the full ceiling. A never-run job (cost 0)
				// gets the full ceiling, not the floor, so first runs measure
				// honestly. Set cost_timeout_factor to 0 to disable.
				CostTimeoutFactor:  3,
				CostTimeoutFloorMs: 5000,
				// Fast/slow lane hysteresis band + reservation (spec
				// 2026-07-01-03): promote to the slow lane at a 2s cost EWMA,
				// demote back below 1s, and keep 5 of the pool's runner slots
				// off-limits to slow jobs. The migration-009 backfill uses the
				// same 2000ms promote threshold.
				LaneSlowThresholdMs: 2000,
				LaneFastThresholdMs: 1000,
				FastLaneReserved:    5,
			},
			RateLimiting: DefaultRateLimitConfig(),
			// One CNAME, pointing at the plain instance target. See
			// ServerConfig.CustomDomainCNAMEMode for the token-mode trade-off.
			CustomDomainCNAMEMode: string(domainverify.ModeShared),
		},
		// In-server ACME is opt-in; the listen addresses are pre-filled so
		// enabling it is a one-flag change on a host with :80/:443 free.
		ACME: ACMEConfig{
			Enabled:     false,
			ListenHTTP:  DefaultACMEListenHTTP,
			ListenHTTPS: DefaultACMEListenHTTPS,
			// Only meaningful once an upstream is configured, and then it is
			// what an operator wants: a chained hop that hides the client from
			// the downstream would collapse its rate limiting to one bucket.
			FallbackUpstreamProxyProtocol: true,
		},
		Database: DatabaseConfig{
			Type:               DatabaseTypeSQLite,
			Dir:                ".",
			MigrationGuardMode: MigrationGuardModeStrict,
			MaxOpenConns:       dbPoolMaxOpenConnsDefault,
			MaxIdleConns:       dbPoolMaxIdleConnsDefault,
			ConnMaxLifetime:    time.Hour,
			ConnMaxIdleTime:    5 * time.Minute,
			// Comfortably above healthy queries here (the post-fix uptime-bar
			// tiers measure ~10ms and ~97ms) and below anything a user would
			// call slow (spec 2026-08-17-04).
			SlowQueryThreshold: 500 * time.Millisecond,
		},
		Auth: AuthConfig{
			JWTSecret:          "change-me-in-production",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
			WebAuthn: WebAuthnConfig{
				Enabled:       true,
				RPDisplayName: "SolidPing",
			},
			// Defaults reproduce the historical hardcoded argon2id profile
			// (m=64 MiB, t=3, p=4, 32-byte key, 16-byte salt) so upgrading
			// changes nothing until reconfigured.
			Password: PasswordConfig{
				Algorithm: PasswordAlgorithmArgon2id,
				Argon2: Argon2Params{
					Memory:     64 * 1024,
					Time:       3,
					Threads:    4,
					KeyLength:  32,
					SaltLength: 16,
				},
				Bcrypt:        BcryptParams{Cost: 12},
				RehashOnLogin: true,
			},
		},
		Email: EmailConfig{
			Port:     587,
			Protocol: "starttls",
			Enabled:  false,
		},
		Aggregation: AggregationConfig{
			RetentionRaw:  24,
			RetentionHour: 7,
			RetentionDay:  2,
		},
		Jobs: JobsConfig{
			StuckTimeout:   15 * time.Minute,
			ReaperInterval: time.Minute,
		},
		FileStorage: FileStorageConfig{
			Type:      "local",
			LocalRoot: "./data/files",
		},
		App: AppConfig{
			GitHub: AppGitHubConfig{
				Repo: "fclairamb/solidping",
			},
		},
		Google:    GoogleOAuthConfig{Enabled: false},
		GitHub:    GitHubOAuthConfig{Enabled: false},
		GitLab:    GitLabOAuthConfig{Enabled: false},
		Microsoft: MicrosoftOAuthConfig{Enabled: false},
		Slack:     SlackConfig{Enabled: false},
		MSTeams:   MSTeamsConfig{Enabled: false},
		// Off by default. Credentials are instance-level and must be supplied
		// explicitly; the template/version defaults only matter once they are.
		WhatsApp: WhatsAppConfig{
			Enabled:          false,
			APIVersion:       DefaultWhatsAppAPIVersion,
			AlertTemplate:    DefaultWhatsAppAlertTemplate,
			VerifyTemplate:   DefaultWhatsAppVerifyTemplate,
			TemplateLanguage: DefaultWhatsAppTemplateLanguage,
		},
		// Off by default. The bot token / username are instance-level and must
		// be supplied explicitly.
		// Enabled left nil on purpose: unset means "auto", i.e. on iff a bot
		// token is supplied. Defaulting it to false would resurrect the
		// ceremony of a second variable just to confirm the token.
		Telegram: TelegramConfig{},
		// Enabled defaults to true but is inert on its own: PostHogConfig.Active
		// additionally requires a project API key, which self-hosted installs
		// never have unless the operator sets one. Host is left empty on purpose
		// so the dashboard captures through the first-party PostHogProxyPath; see
		// BrowserAPIHost.
		PostHog: PostHogConfig{Enabled: true},
		// TracesSampleRate defaults to 0.0, explicitly (not a leftover Go zero
		// value). SolidPing already ships OpenTelemetry tracing behind
		// SP_OTEL_ENABLED; paying Sentry's transaction quota for a second,
		// thinner trace stream is duplicate spend for a self-hostable product
		// where the operator may have no Sentry plan at all. Errors and
		// panics — what Sentry is actually good at — are captured at 100%
		// regardless of this setting.
		Sentry:  SentryConfig{TracesSampleRate: 0.0},
		Discord: DiscordOAuthConfig{Enabled: false},
		OIDC:    OIDCOAuthConfig{Enabled: false},
		SAML:    SAMLConfig{Enabled: false},
		LDAP:    LDAPConfig{Enabled: false},
		Node: NodeConfig{
			Role:   NodeRoleAll,
			Region: "",
		},
		Agent: AgentConfig{
			KeysFile: "/data/agent-keys.json",
		},
		Profiler: ProfilerConfig{
			Enabled: false,
			Listen:  "localhost:6060",
		},
		Runtime: RuntimeConfig{
			AutoMemoryLimit:  true,
			MemoryLimitRatio: 0.9,
		},
		Prometheus: PrometheusConfig{
			Enabled: true,
			Path:    "/metrics",
		},
		Realtime: RealtimeConfig{
			Enabled:                       true,
			FlushInterval:                 time.Second,
			PingInterval:                  25 * time.Second,
			MaxConnections:                1000,
			MaxSubscriptionsPerConnection: 512,
		},
		Encryption: EncryptionConfig{
			AutoMigrate: true,
		},
		Deployment: DeploymentConfig{
			Mode: DeploymentModeSelfHosted,
		},
		Entitlements: EntitlementsConfig{
			SMSRunawayPerHour:  30,
			CallRunawayPerHour: 10,
		},
	}

	if err := koanfInstance.Load(structs.Provider(defaults, "koanf"), nil); err != nil {
		return nil, fmt.Errorf("loading defaults: %w", err)
	}

	// Load from config file if it exists
	if _, err := os.Stat("config.yml"); err == nil {
		if err := koanfInstance.Load(file.Provider("config.yml"), yaml.Parser()); err != nil {
			return nil, fmt.Errorf("loading config.yml: %w", err)
		}
	}

	// Load local overrides (gitignored, for credentials and dev settings)
	if _, err := os.Stat("config.local.yml"); err == nil {
		if err := koanfInstance.Load(file.Provider("config.local.yml"), yaml.Parser()); err != nil {
			return nil, fmt.Errorf("loading config.local.yml: %w", err)
		}
	}

	// Load from environment variables with SP_ prefix (SolidPing)
	if err := koanfInstance.Load(env.Provider(".", env.Opt{
		Prefix: "SP_",
		TransformFunc: func(key, value string) (string, any) {
			path := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(key, "SP_"), "_", "."))

			// telegram.enabled is a *bool tri-state where nil (auto) and false
			// (kill switch) mean opposite things, and koanf cannot express nil:
			// an empty value decodes to false and a non-boolean one fails the
			// whole Unmarshal. Drop it here (an empty key is skipped by the
			// provider) so applyTelegramEnabledEnv is its single reader.
			if path == telegramEnabledKoanfPath {
				return "", nil
			}

			return path, value
		},
	}), nil); err != nil {
		return nil, fmt.Errorf("loading environment variables: %w", err)
	}

	var cfg Config
	if err := koanfInstance.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}

	// Adding port from environment variable if it exists
	if envPort := os.Getenv("PORT"); envPort != "" {
		cfg.Server.Listen = ":" + envPort
	}

	// Parse redirects from SP_REDIRECTS environment variable
	cfg.Server.Redirects = parseRedirects(os.Getenv("SP_REDIRECTS"))

	// Manually read SP_RUN_MODE since it contains an underscore that gets converted to a dot
	if runMode := os.Getenv("SP_RUN_MODE"); runMode != "" {
		cfg.RunMode = runMode
	}

	// Manually read SP_REGION for worker region configuration
	if region := os.Getenv("SP_REGION"); region != "" {
		cfg.Server.CheckWorker.Region = region
	}

	// Manually read the entitlements runaway caps — the underscores in these
	// key segments are converted to dots by the env TransformFunc, so they
	// never reach the koanf `*_per_hour` tags automatically.
	applyEntitlementsEnv(&cfg.Entitlements)

	// If node region is set, also set the check worker region if not already set
	if cfg.Node.Region != "" && cfg.Server.CheckWorker.Region == "" {
		cfg.Server.CheckWorker.Region = cfg.Node.Region
	}

	// Default SP_REGION to "default" if unset
	if cfg.Server.CheckWorker.Region == "" {
		cfg.Server.CheckWorker.Region = "default"
	}

	// Manually read SP_SHUTDOWN_TIMEOUT for shutdown timeout configuration
	if shutdownTimeout := os.Getenv("SP_SHUTDOWN_TIMEOUT"); shutdownTimeout != "" {
		if d, err := time.ParseDuration(shutdownTimeout); err == nil {
			cfg.Server.ShutdownTimeout = d
		}
	}

	// Manually read SP_SERVER_MAX_REQUEST_DURATION — koanf's env loader
	// collapses every underscore in SP_*-prefixed names to a dot, so it
	// would miss the snake_case koanf tag "max_request_duration".
	if v := os.Getenv("SP_SERVER_MAX_REQUEST_DURATION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Server.MaxRequestDuration = d
		}
	}

	applyRateLimitingEnv(&cfg.Server.RateLimiting)
	applyCheckersEnv(&cfg.Checkers)
	applyAgentEnv(&cfg.Agent)
	applyAuthEnv(&cfg.Auth)
	applyPasswordHashingEnv(&cfg.Auth.Password)
	applyFileStorageEnv(&cfg.FileStorage)
	applyWebPushEnv(&cfg.WebPush)
	applyPostHogEnv(&cfg.PostHog)
	applySentryEnv(&cfg.Sentry)
	applyWhatsAppEnv(&cfg.WhatsApp)
	applyTelegramEnv(&cfg.Telegram)
	applySMSEnv(&cfg.SMS)
	applyVoiceEnv(&cfg.Voice)
	applyJobsEnv(&cfg.Jobs)
	applyServerEnv(&cfg.Server)
	applySchedulingEnv(&cfg.Server.Scheduling)
	applyACMEEnv(&cfg.ACME)
	applyProfilerEnv(&cfg.Profiler)
	applyRuntimeEnv(&cfg.Runtime)
	applyRealtimeEnv(&cfg.Realtime)

	// When in test mode and no database type is specified, default to sqlite-memory
	if cfg.RunMode == "test" && cfg.Database.Type == "" {
		cfg.Database.Type = DatabaseTypeSQLiteMemory
	}

	applySentryEnvironmentDefault(&cfg)

	// Manually read SP_DB_RESET for database reset on startup
	if dbReset := os.Getenv("SP_DB_RESET"); dbReset == envTrue || dbReset == "1" {
		cfg.Database.Reset = true
	}

	// cfg.Node.Role is only known now (post-unmarshal), so the role-aware pool
	// default must run here — after config.yml/config.local.yml overrides are
	// already folded into cfg.Database, before the SP_DB_* env overrides below.
	applyNodeRolePoolDefaults(&cfg.Database, cfg.Node.Role)

	applyDatabasePoolEnv(&cfg.Database)
	applyDBSlowQueryEnv(&cfg.Database)
	applyMigrationGuardModeEnv(&cfg.Database)

	// Parse LOG_LEVEL environment variable
	cfg.LogLevel = ParseLogLevel(os.Getenv("SP_LOG_LEVEL"))

	// App / GitHub: SP_APP_GITHUB_ISSUES_TOKEN takes precedence over the
	// bare GITHUB_ISSUES_TOKEN — keeps SP_*-prefixed conventions while
	// allowing CI to reuse the standard token name when SP_* isn't set.
	if v := os.Getenv("SP_APP_GITHUB_ISSUES_TOKEN"); v != "" {
		cfg.App.GitHub.IssuesToken = v
	} else if v := os.Getenv("GITHUB_ISSUES_TOKEN"); v != "" {
		cfg.App.GitHub.IssuesToken = v
	}

	if v := os.Getenv("SP_APP_GITHUB_REPO"); v != "" {
		cfg.App.GitHub.Repo = v
	}

	cfg.App.EnableBugReport = ComputeBugReportEnabled(&cfg.App.GitHub)

	return &cfg, nil
}

// applyRateLimitingEnv reads SP_SERVER_RATE_LIMITING_* into cfg. The koanf env
// loader collapses every underscore in SP_*-prefixed names to a dot, so it
// would map these to server.rate.limiting.* and miss the snake_case koanf tags
// (rate_limiting, requests_per_minute, max_concurrent, trusted_proxies).
func applyRateLimitingEnv(cfg *RateLimitConfig) {
	intEnv := func(name string, dst *int) {
		v := os.Getenv(name)
		if v == "" {
			return
		}
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
	}
	durEnv := func(name string, dst *time.Duration) {
		v := os.Getenv(name)
		if v == "" {
			return
		}
		if d, err := time.ParseDuration(v); err == nil {
			*dst = d
		}
	}
	intEnv("SP_SERVER_RATE_LIMITING_REQUESTS_PER_MINUTE", &cfg.RequestsPerMinute)
	intEnv("SP_SERVER_RATE_LIMITING_BURST", &cfg.Burst)
	intEnv("SP_SERVER_RATE_LIMITING_MAX_CONCURRENT", &cfg.MaxConcurrent)
	intEnv("SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES", &cfg.TrustedProxies)
	intEnv("SP_SERVER_RATE_LIMITING_TOKEN_BUCKETS_PER_IP", &cfg.TokenBucketsPerIP)
	intEnv("SP_SERVER_RATE_LIMITING_RATE_QUEUE", &cfg.RateQueue)
	intEnv("SP_SERVER_RATE_LIMITING_CONCURRENCY_QUEUE", &cfg.ConcurrencyQueue)
	durEnv("SP_SERVER_RATE_LIMITING_MAX_QUEUE_WAIT", &cfg.MaxQueueWait)
}

// applyCheckersEnv reads SP_CHECKERS_BROWSER_* into cfg. koanf's env loader
// collapses every underscore in SP_*-prefixed names to a dot, so it would map
// these onto checkers.browser.cdp.url / checkers.browser.chrome.path and miss
// the snake_case koanf tags (cdp_url, chrome_path) entirely — the variable
// would parse and then silently do nothing. Same quirk as rate_limiting.
//
// checkers.enabled / disabled are single words and stay koanf-reachable;
// enabled_labels is a slice and keeps its existing YAML-only binding.
func applyCheckersEnv(cfg *CheckersConfig) {
	if v := os.Getenv("SP_CHECKERS_BROWSER_CDP_URL"); v != "" {
		cfg.Browser.CDPURL = v
	}

	if v := os.Getenv("SP_CHECKERS_BROWSER_CHROME_PATH"); v != "" {
		cfg.Browser.ChromePath = v
	}
}

// applyPasswordHashingEnv reads SP_AUTH_PASSWORD_* into cfg. koanf's env loader
// collapses every underscore in SP_*-prefixed names to a dot, so multi-word keys
// like SP_AUTH_PASSWORD_ARGON2_MEMORY / ..._KEY_LENGTH / ..._BCRYPT_COST mis-map
// (e.g. auth.password.argon2.key.length). Reading them here keeps the
// password-hashing policy env-configurable, mirroring applyRateLimitingEnv.
// SP_AUTH_PASSWORD_ALGORITHM (single word) is folded in for consistency.
//
// SP_AUTH_PASSWORD_* is read in TWO places, deliberately — this is not an
// accidental double-apply:
//
//  1. Here (config.Load), this is the EARLY / env-only bootstrap path. The policy
//     installed in NewServer (server.go:165) runs before the DB is reachable, so
//     an env-only deployment with no DB-stored params must still pick up its
//     hashing profile from env. This path owns nothing but env.
//  2. systemconfig (Service.Initialize), the AUTHORITATIVE env>DB>default overlay.
//     It re-reads the same SP_AUTH_PASSWORD_* env vars and the DB-stored
//     auth.password.* parameters and re-resolves the policy afterwards
//     (app.reResolvePasswordPolicy). Because env keeps the highest precedence in
//     BOTH paths, re-applying env here is idempotent: env can only ever set the
//     same value the overlay would. A DB-stored value (no env) is invisible to
//     this early path and only takes effect through the overlay — which is exactly
//     the intended division: env-only deployments work at boot, DB-backed
//     overrides win after the overlay.
func applyPasswordHashingEnv(cfg *PasswordConfig) {
	strEnv := func(name string, dst *string) {
		if v := os.Getenv(name); v != "" {
			*dst = v
		}
	}
	u32Env := func(name string, dst *uint32) {
		v := os.Getenv(name)
		if v == "" {
			return
		}
		if n, err := strconv.ParseUint(v, 10, 32); err == nil {
			*dst = uint32(n)
		}
	}
	u8Env := func(name string, dst *uint8) {
		v := os.Getenv(name)
		if v == "" {
			return
		}
		if n, err := strconv.ParseUint(v, 10, 8); err == nil {
			*dst = uint8(n)
		}
	}
	intEnv := func(name string, dst *int) {
		v := os.Getenv(name)
		if v == "" {
			return
		}
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
	}
	boolEnv := func(name string, dst *bool) {
		v := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
		switch v {
		case "true", "1", "yes":
			*dst = true
		case "false", "0", "no":
			*dst = false
		}
	}

	strEnv("SP_AUTH_PASSWORD_ALGORITHM", &cfg.Algorithm)
	u32Env("SP_AUTH_PASSWORD_ARGON2_MEMORY", &cfg.Argon2.Memory)
	u32Env("SP_AUTH_PASSWORD_ARGON2_TIME", &cfg.Argon2.Time)
	u8Env("SP_AUTH_PASSWORD_ARGON2_THREADS", &cfg.Argon2.Threads)
	u32Env("SP_AUTH_PASSWORD_ARGON2_KEY_LENGTH", &cfg.Argon2.KeyLength)
	u32Env("SP_AUTH_PASSWORD_ARGON2_SALT_LENGTH", &cfg.Argon2.SaltLength)
	intEnv("SP_AUTH_PASSWORD_BCRYPT_COST", &cfg.Bcrypt.Cost)
	boolEnv("SP_AUTH_PASSWORD_REHASH_ON_LOGIN", &cfg.RehashOnLogin)
}

// applyJobsEnv reads SP_JOBS_* into cfg. koanf's env loader collapses every
// underscore in SP_*-prefixed names to a dot, so it would map these to
// jobs.stuck.timeout / jobs.reaper.interval and miss the snake_case koanf tags
// ("stuck_timeout", "reaper_interval"). Reading them here keeps the reaper
// timeout/interval env-configurable per project_koanf_env_quirk.
func applyJobsEnv(cfg *JobsConfig) {
	if v := os.Getenv("SP_JOBS_STUCK_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.StuckTimeout = d
		}
	}
	if v := os.Getenv("SP_JOBS_REAPER_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.ReaperInterval = d
		}
	}
}

// applyRealtimeEnv reads the multi-word SP_REALTIME_* knobs koanf's env
// loader cannot bind (it collapses underscores to dots, so
// SP_REALTIME_FLUSH_INTERVAL would map to realtime.flush.interval and miss
// the snake_case koanf tag "flush_interval"). SP_REALTIME_ENABLED is a single
// word and binds through koanf directly. See project_koanf_env_quirk.
func applyRealtimeEnv(cfg *RealtimeConfig) {
	if v := os.Getenv("SP_REALTIME_FLUSH_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.FlushInterval = d
		}
	}
	if v := os.Getenv("SP_REALTIME_PING_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.PingInterval = d
		}
	}
	if v := os.Getenv("SP_REALTIME_MAX_CONNECTIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxConnections = n
		}
	}
	if v := os.Getenv("SP_REALTIME_MAX_SUBSCRIPTIONS_PER_CONNECTION"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxSubscriptionsPerConnection = n
		}
	}
}

// applyAgentEnv reads the SP_AGENT_* knobs manually. koanf's env loader
// collapses underscores to dots, so SP_AGENT_SERVER_URL would map to
// agent.server.url and miss the snake_case koanf tag "server_url" — and
// SP_AGENT_KEYS_FILE maps to agent.keys.file, which would collide with a
// string field bound at agent.keys and fail the whole config unmarshal.
// SP_AGENT_KEYS is therefore read here too (its struct field binds koanf key
// "keys_b64", which no env var reaches). See project_koanf_env_quirk.
func applyAgentEnv(cfg *AgentConfig) {
	if v := os.Getenv("SP_AGENT_SERVER_URL"); v != "" {
		cfg.ServerURL = v
	}

	if v := os.Getenv("SP_AGENT_ENROLLMENT_TOKEN"); v != "" {
		cfg.EnrollmentToken = v
	}

	if v := os.Getenv("SP_AGENT_KEYS_FILE"); v != "" {
		cfg.KeysFile = v
	}

	if v := os.Getenv("SP_AGENT_KEYS"); v != "" {
		cfg.Keys = v
	}

	if v := os.Getenv("SP_AGENT_NAME"); v != "" {
		cfg.Name = v
	}

	if v := os.Getenv("SP_AGENT_PRINT_KEYS"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.PrintKeys = b
		}
	}
}

// applyAuthEnv reads the multi-word SP_AUTH_* token-lifetime knobs koanf's
// env loader cannot bind (it collapses underscores to dots, so
// SP_AUTH_ACCESS_TOKEN_EXPIRY would map to auth.access.token.expiry and miss
// the snake_case koanf tag "access_token_expiry"). Used by
// e2e/session-continuity.spec.ts and similar manual test setups that need a
// short-lived access/refresh token without touching the YAML config. See
// project_koanf_env_quirk.
func applyAuthEnv(cfg *AuthConfig) {
	if v := os.Getenv("SP_AUTH_ACCESS_TOKEN_EXPIRY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.AccessTokenExpiry = d
		}
	}
	if v := os.Getenv("SP_AUTH_REFRESH_TOKEN_EXPIRY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.RefreshTokenExpiry = d
		}
	}
}

// applyServerEnv reads multi-word SP_SERVER_* keys that koanf's env loader
// cannot bind (it collapses underscores to dots, so server.docs_host would
// become server.docs.host). docs_host accepts SP_SERVER_DOCS_HOST and the
// shorter documented SP_DOCS_HOST. See project_koanf_env_quirk.
func applyServerEnv(cfg *ServerConfig) {
	if v := os.Getenv("SP_SERVER_DOCS_HOST"); v != "" {
		cfg.DocsHost = v
	} else if v := os.Getenv("SP_DOCS_HOST"); v != "" {
		cfg.DocsHost = v
	}

	if v := os.Getenv("SP_SERVER_CUSTOM_DOMAIN_CNAME_TARGET"); v != "" {
		cfg.CustomDomainCNAMETarget = v
	} else if v := os.Getenv("SP_CUSTOM_DOMAIN_CNAME_TARGET"); v != "" {
		cfg.CustomDomainCNAMETarget = v
	}

	if v := os.Getenv("SP_SERVER_CUSTOM_DOMAIN_CNAME_MODE"); v != "" {
		cfg.CustomDomainCNAMEMode = v
	} else if v := os.Getenv("SP_CUSTOM_DOMAIN_CNAME_MODE"); v != "" {
		cfg.CustomDomainCNAMEMode = v
	}

	// Literal os.Getenv calls, like every other pair above: the registry guard
	// in envvars_test.go scans this file for them, so a name read through a
	// variable would slip past both the guard and the startup env check.
	if v := os.Getenv("SP_SERVER_EXIT_WITH_PARENT"); v != "" {
		cfg.ExitWithParent = v == envTrue || v == "1"
	} else if v := os.Getenv("SP_EXIT_WITH_PARENT"); v != "" {
		cfg.ExitWithParent = v == envTrue || v == "1"
	}
}

// applyACMEEnv reads the multi-word SP_ACME_* keys koanf's env loader cannot
// bind (it collapses underscores to dots, so acme.ca_url would become
// acme.ca.url). acme.enabled and acme.email are single-word and are bound by
// the auto loader. See project_koanf_env_quirk.
func applyACMEEnv(cfg *ACMEConfig) {
	if v := os.Getenv("SP_ACME_CA_URL"); v != "" {
		cfg.CAURL = v
	}

	if v := os.Getenv("SP_ACME_LISTEN_HTTP"); v != "" {
		cfg.ListenHTTP = v
	}

	if v := os.Getenv("SP_ACME_LISTEN_HTTPS"); v != "" {
		cfg.ListenHTTPS = v
	}

	// Literal os.Getenv calls rather than a loop over a slice of names: the
	// registry guard in envvars_test.go scans this file for them, so a name read
	// indirectly would slip past both the guard and the startup env check.
	if v := os.Getenv("SP_ACME_PROXY_PROTOCOL"); v != "" {
		cfg.ProxyProtocol = v == envTrue || v == "1"
	}

	if v := os.Getenv("SP_ACME_PROXY_PROTOCOL_TRUSTED_CIDRS"); v != "" {
		cfg.ProxyProtocolTrustedCIDRs = splitAndTrim(v)
	}

	if v := os.Getenv("SP_ACME_FALLBACK_UPSTREAM_HTTPS"); v != "" {
		cfg.FallbackUpstreamHTTPS = v
	}

	if v := os.Getenv("SP_ACME_FALLBACK_UPSTREAM_HTTP"); v != "" {
		cfg.FallbackUpstreamHTTP = v
	}

	if v := os.Getenv("SP_ACME_FALLBACK_UPSTREAM_PROXY_PROTOCOL"); v != "" {
		cfg.FallbackUpstreamProxyProtocol = v == envTrue || v == "1"
	}
}

// splitAndTrim turns a comma-separated env value into a slice, dropping empty
// entries so a trailing comma or a stray space cannot become a "" CIDR.
func splitAndTrim(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))

	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}

	return out
}

// applySchedulingEnv reads the multi-word SP_SCHEDULING_* knobs koanf's env
// loader cannot bind (it collapses underscores to dots and would miss the
// snake_case koanf tags like "slow_cost_threshold_ms"). These override the
// defaults set in Load — cost deprioritization and the slow lane are on by
// default; tier weighting and the cost-aware timeout remain opt-in at 0. See
// project_koanf_env_quirk.
func applySchedulingEnv(cfg *SchedulingConfig) {
	parseFloat := func(env string, dst *float64) {
		if v := os.Getenv(env); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				*dst = f
			}
		}
	}

	parseFloat("SP_SCHEDULING_SLOW_THRESHOLD_MS", &cfg.SlowThresholdMs)
	parseFloat("SP_SCHEDULING_CHECK_TIMEOUT_MS", &cfg.CheckTimeoutMs)
	parseFloat("SP_SCHEDULING_TIER_CREDIT_SECONDS", &cfg.TierCreditSeconds)
	parseFloat("SP_SCHEDULING_TIER_CREDIT_MAX_SECONDS", &cfg.TierCreditMaxSeconds)
	parseFloat("SP_SCHEDULING_COST_TIMEOUT_FACTOR", &cfg.CostTimeoutFactor)
	parseFloat("SP_SCHEDULING_COST_TIMEOUT_FLOOR_MS", &cfg.CostTimeoutFloorMs)
	parseFloat("SP_SCHEDULING_LANE_SLOW_THRESHOLD_MS", &cfg.LaneSlowThresholdMs)
	parseFloat("SP_SCHEDULING_LANE_FAST_THRESHOLD_MS", &cfg.LaneFastThresholdMs)
	if v := os.Getenv("SP_SCHEDULING_FAST_LANE_RESERVED"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.FastLaneReserved = n
		}
	}
}

// applyProfilerEnv reads the multi-word SP_PROFILER_* knobs koanf's env loader
// cannot bind (it would map them to profiler.block.rate / profiler.mutex.fraction
// and miss the snake_case koanf tags "block_rate" / "mutex_fraction"). Both are
// opt-in profiling-session levers with runtime cost; default 0 = off. See
// project_koanf_env_quirk.
func applyProfilerEnv(cfg *ProfilerConfig) {
	if v := os.Getenv("SP_PROFILER_BLOCK_RATE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.BlockRate = n
		}
	}
	if v := os.Getenv("SP_PROFILER_MUTEX_FRACTION"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MutexFraction = n
		}
	}
}

// applyNodeRolePoolDefaults swaps in a smaller pool ceiling for checks/jobs
// nodes, unless config.yml or config.local.yml already set an explicit value
// (detected by the pool fields no longer matching the api/all struct-literal
// defaults). Must run after cfg is unmarshaled (cfg.Node.Role is only known
// then) and before applyDatabasePoolEnv, so SP_DB_MAX_OPEN_CONNS /
// SP_DB_MAX_IDLE_CONNS still win over the role default for any role. See
// spec 2026-07-05-09 (D1): a checks/jobs node only runs batched polling loops
// and is typically deployed as several replicas sharing one Postgres role, so
// an API-sized pool per replica saturates the role's connection limit fast.
//
// The rule is expressed on the parsed role set: shrink only for a node that
// runs checks and/or jobs but does NOT serve the API. That is byte-identical to
// the historic exact-string test for every single-value role ("checks"/"jobs"
// shrink; "all"/"api"/"agent"/anything invalid do not), and gives the
// multi-value forms the obvious answer — "api,jobs" keeps the API-sized pool,
// "checks,jobs" gets the worker-sized one.
func applyNodeRolePoolDefaults(cfg *DatabaseConfig, nodeRole string) {
	roles, err := ParseNodeRoles(nodeRole)
	if err != nil {
		return // invalid values are rejected by Validate(); never guess a pool here
	}

	runsWorkerLoops := roles.Runs(NodeRoleChecks) || roles.Runs(NodeRoleJobs)
	if roles.Runs(NodeRoleAPI) || !runsWorkerLoops {
		return
	}

	if cfg.MaxOpenConns == dbPoolMaxOpenConnsDefault {
		cfg.MaxOpenConns = dbPoolMaxOpenConnsChecksDefault
	}

	if cfg.MaxIdleConns == dbPoolMaxIdleConnsDefault {
		cfg.MaxIdleConns = dbPoolMaxIdleConnsChecksDefault
	}
}

// applyDatabasePoolEnv reads the multi-word SP_DB_*_CONNS / SP_DB_CONN_MAX_LIFETIME /
// SP_DB_CONN_MAX_IDLE_TIME knobs koanf's env loader cannot bind (it would collapse
// the underscores to dots and miss the snake_case koanf tags). See
// project_koanf_env_quirk. Runs after applyNodeRolePoolDefaults so these always
// take precedence over the role-based default, for any node role.
func applyDatabasePoolEnv(cfg *DatabaseConfig) {
	if v := os.Getenv("SP_DB_MAX_OPEN_CONNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxOpenConns = n
		}
	}
	if v := os.Getenv("SP_DB_MAX_IDLE_CONNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxIdleConns = n
		}
	}
	if v := os.Getenv("SP_DB_CONN_MAX_LIFETIME"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.ConnMaxLifetime = d
		}
	}
	if v := os.Getenv("SP_DB_CONN_MAX_IDLE_TIME"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.ConnMaxIdleTime = d
		}
	}
}

// applyDBSlowQueryEnv reads the multi-word SP_DB_SLOW_QUERY_THRESHOLD knob
// koanf's env loader cannot bind (it would collapse the underscores to dots
// and miss the snake_case koanf tag "slow_query_threshold"). See
// project_koanf_env_quirk, mirroring applyDatabasePoolEnv.
func applyDBSlowQueryEnv(cfg *DatabaseConfig) {
	if v := os.Getenv("SP_DB_SLOW_QUERY_THRESHOLD"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.SlowQueryThreshold = d
		}
	}
}

// applyMigrationGuardModeEnv reads the multi-word SP_DB_MIGRATION_GUARD_MODE
// knob koanf's env loader cannot bind (it would collapse the underscores to
// dots and miss the snake_case koanf tag "migration_guard_mode"). Validation
// of the value happens in validateDatabaseConfig, not here — an invalid value
// is left in place so Validate reports it rather than being silently dropped.
// See project_koanf_env_quirk.
func applyMigrationGuardModeEnv(cfg *DatabaseConfig) {
	if v := os.Getenv("SP_DB_MIGRATION_GUARD_MODE"); v != "" {
		cfg.MigrationGuardMode = v
	}
}

// applyRuntimeEnv reads the multi-word SP_RUNTIME_* knobs koanf's env loader
// cannot bind (it would collapse the underscores to dots and miss the snake_case
// koanf tags "memory_limit" / "auto_memory_limit" / "memory_limit_ratio" /
// "gc_percent"). See project_koanf_env_quirk.
func applyRuntimeEnv(cfg *RuntimeConfig) {
	if v := os.Getenv("SP_RUNTIME_MEMORY_LIMIT"); v != "" {
		cfg.MemoryLimit = v
	}
	if v := os.Getenv("SP_RUNTIME_AUTO_MEMORY_LIMIT"); v != "" {
		cfg.AutoMemoryLimit = v == envTrue || v == "1"
	}
	if v := os.Getenv("SP_RUNTIME_MEMORY_LIMIT_RATIO"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.MemoryLimitRatio = f
		}
	}
	if v := os.Getenv("SP_RUNTIME_GC_PERCENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.GCPercent = n
		}
	}
}

// applyFileStorageEnv reads SP_FILESTORAGE_S3_* into cfg. koanf's env loader
// collapses every underscore in SP_*-prefixed names to a dot, so it would map
// these to filestorage.s3.bucket / filestorage.s3.endpoint etc. and miss the
// snake_case koanf tags ("s3_bucket", "s3_endpoint", ...). Reading them here
// makes the whole S3 backend env-configurable for containerized self-hosters.
func applyFileStorageEnv(cfg *FileStorageConfig) {
	if v := os.Getenv("SP_FILESTORAGE_S3_BUCKET"); v != "" {
		cfg.S3Bucket = v
	}
	if v := os.Getenv("SP_FILESTORAGE_S3_REGION"); v != "" {
		cfg.S3Region = v
	}
	if v := os.Getenv("SP_FILESTORAGE_S3_PREFIX"); v != "" {
		cfg.S3Prefix = v
	}
	if v := os.Getenv("SP_FILESTORAGE_S3_ENDPOINT"); v != "" {
		cfg.S3Endpoint = v
	}
	if v := os.Getenv("SP_FILESTORAGE_S3_USE_PATH_STYLE"); v == envTrue || v == "1" {
		cfg.S3UsePathStyle = true
	}
	if v := os.Getenv("SP_FILESTORAGE_S3_ACCESS_KEY"); v != "" {
		cfg.S3AccessKey = v
	}
	if v := os.Getenv("SP_FILESTORAGE_S3_SECRET_KEY"); v != "" {
		cfg.S3SecretKey = v
	}
}

// applyWebPushEnv reads SP_WEBPUSH_* into cfg. koanf's env loader collapses
// every underscore in SP_*-prefixed names to a dot, so it maps
// SP_WEBPUSH_VAPID_PUBLIC_KEY to webpush.vapid.public.key instead of
// webpush.vapid_public_key, missing the snake_case koanf tags.
func applyWebPushEnv(cfg *WebPushConfig) {
	if v := os.Getenv("SP_WEBPUSH_VAPID_PUBLIC_KEY"); v != "" {
		cfg.VAPIDPublicKey = v
	}

	if v := os.Getenv("SP_WEBPUSH_VAPID_PRIVATE_KEY"); v != "" {
		cfg.VAPIDPrivateKey = v
	}

	if v := os.Getenv("SP_WEBPUSH_SUBJECT"); v != "" {
		cfg.Subject = v
	}

	// The VAPID "sub" claim is required by some push services (e.g. Firefox's
	// Mozilla push service returns 403 when it is absent). Default to the
	// SolidPing landing page; self-hosted operators can override via
	// SP_WEBPUSH_SUBJECT.
	if cfg.Subject == "" {
		cfg.Subject = "https://solidping.io"
	}

	if v := os.Getenv("SP_WEBPUSH_ENABLED"); v == envTrue || v == "1" {
		cfg.Enabled = true
	}
}

// applyPostHogEnv reads SP_POSTHOG_* into cfg. posthog.enabled and posthog.host
// are koanf-reachable (single-word segments) and already bound by the env
// provider, but posthog.project_api_key / posthog.personal_api_key have
// snake_case segments that koanf's underscore→dot collapsing can never reach,
// so they are read by hand here. Keep in sync with manualReaderPlatformEnvVars.
func applyPostHogEnv(cfg *PostHogConfig) {
	if v := os.Getenv("SP_POSTHOG_PROJECT_API_KEY"); v != "" {
		cfg.ProjectAPIKey = strings.TrimSpace(v)
	}

	if v := os.Getenv("SP_POSTHOG_PERSONAL_API_KEY"); v != "" {
		cfg.PersonalAPIKey = strings.TrimSpace(v)
	}
}

// Sentry environment defaults. An operator who sets only SP_SENTRY_DSN used to
// get events with an empty `environment`, which makes the Sentry UI unable to
// separate a developer's laptop from production — the one split that matters
// there. Deriving it from the run mode means no event is ever
// environment-less.
const (
	runModeTest = "test"

	// SentryEnvironmentTest is the default Sentry environment under
	// SP_RUN_MODE=test.
	SentryEnvironmentTest = "test"
	// SentryEnvironmentProduction is the default Sentry environment otherwise.
	SentryEnvironmentProduction = "production"
)

// applySentryEnvironmentDefault fills sentry.environment when the operator left
// it unset. Explicit configuration always wins: this runs after config.yml and
// the SP_SENTRY_ENVIRONMENT env binding have both been folded in, and only
// assigns to an empty value.
func applySentryEnvironmentDefault(cfg *Config) {
	if cfg.Sentry.Environment != "" {
		return
	}

	if cfg.RunMode == runModeTest {
		cfg.Sentry.Environment = SentryEnvironmentTest

		return
	}

	cfg.Sentry.Environment = SentryEnvironmentProduction
}

// applySentryEnv reads SP_SENTRY_* into cfg. sentry.dsn / sentry.environment /
// sentry.debug are koanf-reachable (single-word segments) and already bound by
// the env provider, but sentry.traces_sample_rate has a snake_case segment
// that koanf's underscore→dot collapsing can never reach, so it is read by
// hand here. Keep in sync with manualReaderPlatformEnvVars.
//
// Do NOT read SP_SENTRY_DSN / SP_SENTRY_ENVIRONMENT / SP_SENTRY_DEBUG here —
// re-reading them by hand would change their precedence against config.yml.
func applySentryEnv(cfg *SentryConfig) {
	v := os.Getenv("SP_SENTRY_TRACES_SAMPLE_RATE")
	if v == "" {
		return
	}

	rate, err := strconv.ParseFloat(v, 64)
	if err != nil {
		// Malformed input: leave the existing value, matching the fail-open
		// convention of the other manual numeric readers (e.g.
		// applyDatabasePoolEnv, applyJobsEnv).
		return
	}

	if rate < 0.0 || rate > 1.0 {
		// Out of range is an operator error, not something to clamp
		// silently — Sentry itself treats >1 as 1 without complaint, which
		// would hide the mistake. Reject and keep the existing value.
		return
	}

	cfg.TracesSampleRate = rate
}

// SP_WHATSAPP_* environment variable names. Declared once and referenced from
// both the manual reader below and the RecognizedEnvVars list in envvars.go, so
// the two can never drift apart.
const (
	EnvWhatsAppAccessToken        = "SP_WHATSAPP_ACCESS_TOKEN"
	EnvWhatsAppPhoneNumberID      = "SP_WHATSAPP_PHONE_NUMBER_ID"
	EnvWhatsAppWABAID             = "SP_WHATSAPP_WABA_ID"
	EnvWhatsAppAppSecret          = "SP_WHATSAPP_APP_SECRET"
	EnvWhatsAppWebhookVerifyToken = "SP_WHATSAPP_WEBHOOK_VERIFY_TOKEN"
	EnvWhatsAppAPIVersion         = "SP_WHATSAPP_API_VERSION"
	EnvWhatsAppAlertTemplate      = "SP_WHATSAPP_ALERT_TEMPLATE"
	EnvWhatsAppVerifyTemplate     = "SP_WHATSAPP_VERIFY_TEMPLATE"
	EnvWhatsAppTemplateLanguage   = "SP_WHATSAPP_TEMPLATE_LANGUAGE"
	EnvWhatsAppBaseURL            = "SP_WHATSAPP_BASE_URL"
	// EnvEntitlementsWhatsAppRunaway caps outbound WhatsApp messages per org
	// per hour, independently of the billing-driven monthly quota.
	EnvEntitlementsWhatsAppRunaway = "SP_ENTITLEMENTS_WHATSAPP_RUNAWAY_PER_HOUR"
)

// WhatsAppEnvVarNames lists every manually-read SP_WHATSAPP_* name, in the
// order they are bound.
func WhatsAppEnvVarNames() []string {
	return []string{
		EnvWhatsAppAccessToken, EnvWhatsAppPhoneNumberID, EnvWhatsAppWABAID,
		EnvWhatsAppAppSecret, EnvWhatsAppWebhookVerifyToken, EnvWhatsAppAPIVersion,
		EnvWhatsAppAlertTemplate, EnvWhatsAppVerifyTemplate,
		EnvWhatsAppTemplateLanguage, EnvWhatsAppBaseURL,
	}
}

// applyEntitlementsEnv reads the per-org hourly runaway caps. Every key has a
// snake_case segment koanf's env provider cannot reach, so they are read here.
// Keep in sync with manualReaderEnvVars.
func applyEntitlementsEnv(cfg *EntitlementsConfig) {
	if v := envInt("SP_ENTITLEMENTS_SMS_RUNAWAY_PER_HOUR"); v > 0 {
		cfg.SMSRunawayPerHour = v
	}

	if v := envInt("SP_ENTITLEMENTS_CALL_RUNAWAY_PER_HOUR"); v > 0 {
		cfg.CallRunawayPerHour = v
	}

	if v := envInt(EnvEntitlementsWhatsAppRunaway); v > 0 {
		cfg.WhatsAppRunawayPerHour = v
	}

	if v := envInt(EnvEntitlementsTelegramRunaway); v > 0 {
		cfg.TelegramRunawayPerHour = v
	}
}

// applyWhatsAppEnv reads SP_WHATSAPP_* into cfg. Only whatsapp.enabled is
// koanf-reachable (single-word segment); every other key has a snake_case
// segment that koanf's underscore→dot collapsing can never reach
// (SP_WHATSAPP_PHONE_NUMBER_ID would land on whatsapp.phone.number.id), so they
// are read by hand here — the same quirk as rate_limiting and posthog.
// Keep in sync with manualReaderPlatformEnvVars.
func applyWhatsAppEnv(cfg *WhatsAppConfig) {
	targets := []*string{
		&cfg.AccessToken, &cfg.PhoneNumberID, &cfg.WABAID,
		&cfg.AppSecret, &cfg.WebhookVerifyToken, &cfg.APIVersion,
		&cfg.AlertTemplate, &cfg.VerifyTemplate,
		&cfg.TemplateLanguage, &cfg.BaseURL,
	}

	for i, name := range WhatsAppEnvVarNames() {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			*targets[i] = v
		}
	}
}

// SP_TELEGRAM_* environment variable names. Declared once and referenced from
// both the manual reader below and the RecognizedEnvVars list in envvars.go, so
// the two can never drift apart.
const (
	// EnvTelegramEnabled is the tri-state kill switch. Read by hand (see
	// applyTelegramEnabledEnv) even though koanf CAN reach it, because koanf
	// cannot distinguish "unset" from "set to empty" and that distinction is
	// load-bearing here.
	EnvTelegramEnabled = "SP_TELEGRAM_ENABLED"
	// telegramEnabledKoanfPath is the koanf path EnvTelegramEnabled would land
	// on. The env provider deliberately drops it; see the TransformFunc.
	telegramEnabledKoanfPath = "telegram.enabled"

	EnvTelegramBotToken      = "SP_TELEGRAM_BOT_TOKEN"
	EnvTelegramBotUsername   = "SP_TELEGRAM_BOT_USERNAME"
	EnvTelegramWebhookSecret = "SP_TELEGRAM_WEBHOOK_SECRET"
	EnvTelegramBaseURL       = "SP_TELEGRAM_BASE_URL"
	// EnvEntitlementsTelegramRunaway caps outbound Telegram messages per org
	// per hour. There is no monthly quota for Telegram (the channel is free),
	// so this guard is the only bound on a runaway dispatch loop.
	EnvEntitlementsTelegramRunaway = "SP_ENTITLEMENTS_TELEGRAM_RUNAWAY_PER_HOUR"
)

// TelegramEnvVarNames lists every manually-read SP_TELEGRAM_* name, in the
// order they are bound.
func TelegramEnvVarNames() []string {
	return []string{
		EnvTelegramBotToken, EnvTelegramBotUsername,
		EnvTelegramWebhookSecret, EnvTelegramBaseURL,
	}
}

// applyTelegramEnv reads SP_TELEGRAM_* into cfg. Every key here has a
// snake_case segment that koanf's underscore→dot collapsing can never reach
// (SP_TELEGRAM_BOT_TOKEN would land on telegram.bot.token), so they are read by
// hand — the same quirk as rate_limiting, posthog and whatsapp.
//
// telegram.enabled is the exception that koanf COULD reach, but it is dropped
// from the env provider on purpose and read here too; see
// applyTelegramEnabledEnv for why.
// Keep in sync with manualReaderPlatformEnvVars.
func applyTelegramEnv(cfg *TelegramConfig) {
	targets := []*string{
		&cfg.BotToken, &cfg.BotUsername,
		&cfg.WebhookSecret, &cfg.BaseURL,
	}

	for i, name := range TelegramEnvVarNames() {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			*targets[i] = v
		}
	}

	applyTelegramEnabledEnv(cfg)
}

// applyTelegramEnabledEnv re-reads SP_TELEGRAM_ENABLED by hand, overriding what
// koanf's env provider already decoded.
//
// telegram.enabled IS koanf-reachable (single-word segment), unlike its
// siblings — but that path cannot tell "unset" from "set to empty": koanf hands
// mapstructure an empty string and the weakly-typed string→bool conversion
// turns it into false, i.e. *Enabled == false. For an ordinary bool that is
// harmless; here nil (auto) versus false (kill switch) is the whole point of
// the tri-state, and false wins over a perfectly valid bot token.
//
// That matters because a bare `SP_TELEGRAM_ENABLED=` line is exactly what a
// dotenv file produces — `set -a; . .env`, `docker run --env-file`, most PaaS
// dotenv importers — so an operator who copied .env.example and filled in only
// the bot token would get Telegram silently and completely off. An empty value
// means "I did not choose", never "off".
//
// An absent variable is left alone so a config-file or default value still
// applies; only an explicitly present one is honored here.
func applyTelegramEnabledEnv(cfg *TelegramConfig) {
	raw, present := os.LookupEnv(EnvTelegramEnabled)
	if !present {
		return
	}

	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		cfg.Enabled = nil

		return
	}

	parsed, err := strconv.ParseBool(trimmed)
	if err != nil {
		slog.Warn("ignoring unparseable "+EnvTelegramEnabled+
			"; falling back to auto (on iff a bot token is present)",
			"value", trimmed)

		cfg.Enabled = nil

		return
	}

	cfg.Enabled = &parsed
}

// ComputeBugReportEnabled returns true iff a GitHub PAT and repo are configured.
// Used at startup and after a system-parameter reload of app.github.* keys.
func ComputeBugReportEnabled(gh *AppGitHubConfig) bool {
	return gh.IssuesToken != "" && gh.Repo != ""
}

// Validate checks that the configuration is valid and returns an error if not.
func (c *Config) Validate() error {
	if err := validateDatabaseConfig(&c.Database); err != nil {
		return err
	}

	if err := c.validateNodeConfig(); err != nil {
		return err
	}

	// Validate aggregation retention values are positive
	if c.Aggregation.RetentionRaw < 1 ||
		c.Aggregation.RetentionHour < 1 ||
		c.Aggregation.RetentionDay < 1 {
		return fmt.Errorf("%w: raw=%d hour=%d day=%d",
			ErrInvalidAggregationRetention,
			c.Aggregation.RetentionRaw,
			c.Aggregation.RetentionHour,
			c.Aggregation.RetentionDay)
	}

	// Validate deployment mode (empty = self-hosted, set in defaults).
	if c.Deployment.Mode == "" {
		c.Deployment.Mode = DeploymentModeSelfHosted
	}
	if !slices.Contains(ValidDeploymentModes(), c.Deployment.Mode) {
		return fmt.Errorf("%w, got '%s'", ErrInvalidDeploymentMode, c.Deployment.Mode)
	}

	if err := validatePasswordConfig(&c.Auth.Password); err != nil {
		return err
	}

	if err := validateLaneThresholds(&c.Server.Scheduling); err != nil {
		return err
	}

	if err := validateCustomDomainTLSConfig(&c.Server, &c.ACME); err != nil {
		return err
	}

	return nil
}

// validateNodeConfig validates the node block: the role itself, the
// role-specific requirements, and the worker identity this process would
// register under.
func (c *Config) validateNodeConfig() error {
	// An unknown role, a malformed list, or a conflicting combination aborts
	// startup here: silently disabling a subsystem is the failure mode this
	// guard exists to prevent.
	roles, err := ParseNodeRoles(c.Node.Role)
	if err != nil {
		return err
	}

	// The checks role needs a region. Membership is deliberately literal (Has,
	// not Runs): "all" has never required SP_NODE_REGION, and must not start.
	if roles.Has(NodeRoleChecks) && c.Node.Region == "" {
		return ErrRegionRequiredForChecks
	}

	// The agent role needs a server URL (the enrollment token is only needed on
	// the very first run — a persisted identity replaces it).
	if roles.Has(NodeRoleAgent) && c.Agent.ServerURL == "" {
		return ErrAgentServerURLRequired
	}

	// Any node that registers a `workers` row (check worker and/or job worker)
	// must carry a slug the database CHECK constraint accepts. Validating here
	// turns an opaque SQLSTATE=23514 at INSERT time into a startup error naming
	// the offending value and SP_NODE_NAME.
	if c.ShouldRunChecks() || c.ShouldRunJobs() {
		if err := c.WorkerIdentity().Validate(); err != nil {
			return err
		}
	}

	return nil
}

// validateCustomDomainTLSConfig validates everything custom-domain serving
// depends on: the CNAME verification mode and, when in-server TLS is enabled,
// the ACME block.
func validateCustomDomainTLSConfig(server *ServerConfig, acme *ACMEConfig) error {
	if _, ok := domainverify.ParseMode(server.CustomDomainCNAMEMode); !ok {
		return fmt.Errorf("%w, got '%s'", ErrInvalidCNAMEMode, server.CustomDomainCNAMEMode)
	}

	return validateACMEConfig(acme)
}

// validateACMEConfig fails fast when in-server TLS is switched on without the
// inputs it cannot invent: Let's Encrypt refuses an account with no contact
// address, and both listeners are mandatory (HTTP-01 needs the :80 listener,
// TLS-ALPN-01 and actually serving need the :443 one).
func validateACMEConfig(cfg *ACMEConfig) error {
	// Checked before the enabled gate: a fallback upstream set on a disabled
	// edge forwards nothing at all, which is exactly the misconfiguration that
	// would look like "the chain silently drops every unknown host".
	if err := validateACMEFallbackUpstreams(cfg); err != nil {
		return err
	}

	if !cfg.Enabled {
		return nil
	}

	if strings.TrimSpace(cfg.Email) == "" {
		return ErrACMEEmailRequired
	}

	if strings.TrimSpace(cfg.ListenHTTP) == "" || strings.TrimSpace(cfg.ListenHTTPS) == "" {
		return ErrACMEListenRequired
	}

	return validateACMEProxyProtocol(cfg)
}

// validateACMEFallbackUpstreams enforces that a configured next hop is both
// dialable and reachable by the code that would use it: a "host:port" address
// with a non-empty host and a numeric port, on an edge that actually listens.
// The alternative is a chain that looks configured and silently drops every
// unknown-host connection.
func validateACMEFallbackUpstreams(cfg *ACMEConfig) error {
	for _, upstream := range []string{cfg.FallbackUpstreamHTTPS, cfg.FallbackUpstreamHTTP} {
		trimmed := strings.TrimSpace(upstream)
		if trimmed == "" {
			continue
		}

		if !cfg.Enabled {
			return fmt.Errorf("%w, got '%s'", ErrACMEFallbackUpstreamRequiresACME, trimmed)
		}

		host, port, err := net.SplitHostPort(trimmed)
		if err != nil || strings.TrimSpace(host) == "" {
			return fmt.Errorf("%w, got '%s'", ErrACMEFallbackUpstreamInvalid, upstream)
		}

		if portNum, convErr := strconv.Atoi(port); convErr != nil || portNum <= 0 || portNum > maxTCPPort {
			return fmt.Errorf("%w, got '%s'", ErrACMEFallbackUpstreamInvalid, upstream)
		}
	}

	return nil
}

// validateACMEProxyProtocol enforces the trust policy's precondition: PROXY
// protocol support may only be switched on together with an explicit list of
// sources whose header is honored. An empty list would mean "trust every peer",
// i.e. let anyone who can open a TCP connection dictate the client IP the rate
// limiter and the abuse logs see.
func validateACMEProxyProtocol(cfg *ACMEConfig) error {
	if !cfg.ProxyProtocol {
		return nil
	}

	trusted := make([]string, 0, len(cfg.ProxyProtocolTrustedCIDRs))

	for _, entry := range cfg.ProxyProtocolTrustedCIDRs {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}

		// Same shape go-proxyproto accepts: a "/" means a CIDR range, anything
		// else must be a bare IP address.
		if strings.Contains(trimmed, "/") {
			if _, _, err := net.ParseCIDR(trimmed); err != nil {
				return fmt.Errorf("%w, got '%s'", ErrACMEProxyProtocolCIDRInvalid, entry)
			}
		} else if net.ParseIP(trimmed) == nil {
			return fmt.Errorf("%w, got '%s'", ErrACMEProxyProtocolCIDRInvalid, entry)
		}

		trusted = append(trusted, trimmed)
	}

	if len(trusted) == 0 {
		return ErrACMEProxyProtocolCIDRsRequired
	}

	return nil
}

// validateLaneThresholds fails fast on an inverted or degenerate lane
// hysteresis band (spec 2026-07-01-03): with the classifier enabled
// (lane_slow_threshold_ms > 0), the demote edge must sit strictly below the
// promote edge — fast >= slow would make every classified check satisfy the
// promote rule and never the demote one (or vice versa), silently pinning the
// fleet into one lane. Negative thresholds are rejected outright. A slow
// threshold of 0 disables classification, in which case the fast threshold is
// ignored. FastLaneReserved is not validated here: its bound depends on the
// worker pool size, so the worker clamps it (with a warning) at startup.
func validateLaneThresholds(cfg *SchedulingConfig) error {
	if cfg.LaneSlowThresholdMs < 0 || cfg.LaneFastThresholdMs < 0 {
		return fmt.Errorf("%w: fast=%v slow=%v",
			ErrInvalidLaneThresholds, cfg.LaneFastThresholdMs, cfg.LaneSlowThresholdMs)
	}
	if cfg.LaneSlowThresholdMs > 0 && cfg.LaneFastThresholdMs >= cfg.LaneSlowThresholdMs {
		return fmt.Errorf("%w: fast=%v slow=%v",
			ErrInvalidLaneThresholds, cfg.LaneFastThresholdMs, cfg.LaneSlowThresholdMs)
	}

	return nil
}

// validateDatabaseConfig checks the database type and its type-specific
// requirements (postgres needs a URL, on-disk sqlite needs a directory).
func validateDatabaseConfig(cfg *DatabaseConfig) error {
	validTypes := []string{
		DatabaseTypePostgres,
		DatabaseTypePostgresEmbedded,
		DatabaseTypeSQLite,
		DatabaseTypeSQLiteMemory,
	}

	if !slices.Contains(validTypes, cfg.Type) {
		return fmt.Errorf("%w, got '%s'", ErrInvalidDatabaseType, cfg.Type)
	}

	// Postgres requires a URL.
	if cfg.Type == DatabaseTypePostgres && cfg.URL == "" {
		return ErrDatabaseURLRequired
	}

	// On-disk SQLite requires a directory (memory mode does not).
	if cfg.Type == DatabaseTypeSQLite && cfg.Dir == "" {
		return ErrDatabaseDirRequired
	}

	if cfg.MigrationGuardMode != MigrationGuardModeStrict && cfg.MigrationGuardMode != MigrationGuardModeWarn {
		return fmt.Errorf("%w, got '%s'", ErrInvalidMigrationGuardMode, cfg.MigrationGuardMode)
	}

	return nil
}

// ValidatePasswordConfigBlock validates a fully-resolved password config block
// against the same bounds enforced at config load. It is exported so the startup
// policy re-resolve (after the system-parameter overlay) can reject a malformed
// stored value and keep the prior policy instead of installing a degraded one.
func ValidatePasswordConfigBlock(pwCfg *PasswordConfig) error {
	return validatePasswordConfig(pwCfg)
}

// validatePasswordConfig fails fast at config load for an unsupported algorithm
// or sub-floor cost parameters, so a misconfiguration can never lock everyone
// out at first login. Near-floor (but accepted) values are warn-logged.
func validatePasswordConfig(pwCfg *PasswordConfig) error {
	// An empty algorithm means "unset" and resolves to the argon2id default at
	// policy-resolution time; validating an unset/zero-value block as argon2id
	// would reject the legitimate zero values, so accept it here.
	if pwCfg.Algorithm == "" {
		return nil
	}

	switch pwCfg.Algorithm {
	case PasswordAlgorithmArgon2id:
		if err := validateArgon2Params(&pwCfg.Argon2); err != nil {
			return err
		}
	case PasswordAlgorithmBcrypt:
		if pwCfg.Bcrypt.Cost < bcryptCostMin || pwCfg.Bcrypt.Cost > bcryptCostMax {
			return fmt.Errorf("%w, got %d", ErrInvalidBcryptCost, pwCfg.Bcrypt.Cost)
		}
		if pwCfg.Bcrypt.Cost < bcryptCostAdvisory {
			slog.Warn("bcrypt cost is below the recommended value",
				"cost", pwCfg.Bcrypt.Cost, "recommended", bcryptCostAdvisory)
		}
	default:
		return fmt.Errorf("%w, got '%s'", ErrInvalidPasswordAlgorithm, pwCfg.Algorithm)
	}

	return nil
}

// validateArgon2Params rejects sub-floor argon2id parameters and warn-logs
// memory below the OWASP floor (which is allowed).
func validateArgon2Params(params *Argon2Params) error {
	if params.Memory < argon2MemoryFloorKiB ||
		params.Time < argon2TimeFloor ||
		params.Threads < argon2ThreadsFloor ||
		params.KeyLength < argon2KeyLengthFloor ||
		params.SaltLength < argon2SaltLenFloor {
		return fmt.Errorf("%w: memory=%d(min %d KiB) time=%d threads=%d key_length=%d salt_length=%d",
			ErrInvalidArgon2Params,
			params.Memory, argon2MemoryFloorKiB,
			params.Time, params.Threads, params.KeyLength, params.SaltLength)
	}
	if params.Memory < argon2MemoryOWASPKiB {
		slog.Warn("argon2id memory is below the OWASP floor; offline-crack resistance is reduced",
			"memoryKiB", params.Memory, "owaspFloorKiB", argon2MemoryOWASPKiB)
	}

	return nil
}

// Known auth.password.* parameter keys, exported so the system-parameter write
// handler can validate them against the same bounds used at config load.
const (
	ParamKeyPasswordAlgorithm     = "auth.password.algorithm"
	ParamKeyPasswordArgon2Memory  = "auth.password.argon2.memory"
	ParamKeyPasswordArgon2Time    = "auth.password.argon2.time"
	ParamKeyPasswordArgon2Threads = "auth.password.argon2.threads"
	ParamKeyPasswordArgon2KeyLen  = "auth.password.argon2.key_length"
	ParamKeyPasswordArgon2SaltLen = "auth.password.argon2.salt_length"
	ParamKeyPasswordBcryptCost    = "auth.password.bcrypt.cost"
	ParamKeyPasswordRehashOnLogin = "auth.password.rehash_on_login"
)

// IsPasswordParameterKey reports whether key is one of the validated
// auth.password.* system parameters.
func IsPasswordParameterKey(key string) bool {
	switch key {
	case ParamKeyPasswordAlgorithm, ParamKeyPasswordArgon2Memory, ParamKeyPasswordArgon2Time,
		ParamKeyPasswordArgon2Threads, ParamKeyPasswordArgon2KeyLen, ParamKeyPasswordArgon2SaltLen,
		ParamKeyPasswordBcryptCost, ParamKeyPasswordRehashOnLogin:
		return true
	default:
		return false
	}
}

// paramToInt coerces a JSON-decoded system-parameter value (float64 from
// encoding/json, or native int) to an int. Returns ok=false on any other type.
func paramToInt(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	default:
		return 0, false
	}
}

// ValidatePasswordParameter validates a single auth.password.* value against the
// exact bounds enforced by Config.Validate, so a value saved through the
// system-parameter API can never produce a config that aborts the next startup.
// It is the single source of truth shared by the write handler and config load.
func ValidatePasswordParameter(key string, value any) error {
	switch key {
	case ParamKeyPasswordAlgorithm:
		algo, ok := value.(string)
		if !ok || (algo != PasswordAlgorithmArgon2id && algo != PasswordAlgorithmBcrypt) {
			return fmt.Errorf("%w: %s must be 'argon2id' or 'bcrypt'", ErrInvalidPasswordParameter, key)
		}
	case ParamKeyPasswordArgon2Memory:
		return validatePasswordIntBound(key, value, argon2MemoryFloorKiB)
	case ParamKeyPasswordArgon2Time:
		return validatePasswordIntBound(key, value, argon2TimeFloor)
	case ParamKeyPasswordArgon2Threads:
		return validatePasswordIntRange(key, value, argon2ThreadsFloor, argon2ThreadsMax)
	case ParamKeyPasswordArgon2KeyLen:
		return validatePasswordIntBound(key, value, argon2KeyLengthFloor)
	case ParamKeyPasswordArgon2SaltLen:
		return validatePasswordIntBound(key, value, argon2SaltLenFloor)
	case ParamKeyPasswordBcryptCost:
		return validatePasswordIntRange(key, value, bcryptCostMin, bcryptCostMax)
	case ParamKeyPasswordRehashOnLogin:
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%w: %s must be a boolean", ErrInvalidPasswordParameter, key)
		}
	default:
		return fmt.Errorf("%w: unknown key %s", ErrInvalidPasswordParameter, key)
	}

	return nil
}

// validatePasswordIntBound rejects a value below floor (inclusive lower bound only).
func validatePasswordIntBound(key string, value any, floor int) error {
	n, ok := paramToInt(value)
	if !ok {
		return fmt.Errorf("%w: %s must be a number", ErrInvalidPasswordParameter, key)
	}
	if n < floor {
		return fmt.Errorf("%w: %s must be >= %d, got %d", ErrInvalidPasswordParameter, key, floor, n)
	}

	return nil
}

// validatePasswordIntRange rejects a value outside [low, high].
func validatePasswordIntRange(key string, value any, low, high int) error {
	n, ok := paramToInt(value)
	if !ok {
		return fmt.Errorf("%w: %s must be a number", ErrInvalidPasswordParameter, key)
	}
	if n < low || n > high {
		return fmt.Errorf("%w: %s must be between %d and %d, got %d", ErrInvalidPasswordParameter, key, low, high, n)
	}

	return nil
}

// parseRedirects parses the SP_REDIRECTS environment variable.
// Format: /path:host:port/targetpath,/path2:host2:port2/targetpath2.
// Example: /dashboard:localhost:5173/dashboard,/status:localhost:5174/status.
// envInt reads an integer environment variable, returning 0 when unset or
// unparseable (callers treat 0 as "leave the default").
func envInt(key string) int {
	raw := os.Getenv(key)
	if raw == "" {
		return 0
	}

	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}

	return n
}

func parseRedirects(value string) []RedirectRule {
	if value == "" {
		return nil
	}

	slog.Info("Redirects rules set", "rules", value)

	parts := strings.Split(value, ",")
	rules := make([]RedirectRule, 0, len(parts))

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		rule, ok := parseRedirectRule(part)
		if !ok {
			slog.Warn("Invalid redirect rule, skipping", "rule", part)
			continue
		}

		rules = append(rules, rule)
	}

	// Sort by path prefix length descending (longest first for correct matching)
	sort.Slice(rules, func(i, j int) bool {
		return len(rules[i].PathPrefix) > len(rules[j].PathPrefix)
	})

	if len(rules) > 0 {
		slog.Info("Loaded redirect rules", "count", len(rules))

		for i := range rules {
			r := &rules[i]
			slog.Debug("Redirect rule", "pathPrefix", r.PathPrefix, "targetHost", r.TargetHost, "targetPath", r.TargetPath)
		}
	}

	return rules
}

// parseRedirectRule parses a single redirect rule
// Format: /path:host:port/targetpath or /path:host:port
// The path prefix is everything before the first colon
// The target is everything after the first colon, parsed as host:port/path.
func parseRedirectRule(rule string) (RedirectRule, bool) {
	// Must start with /
	if !strings.HasPrefix(rule, "/") {
		return RedirectRule{}, false
	}

	// Find the first colon after the path prefix
	// Path prefix ends at the first colon
	colonIdx := strings.Index(rule, ":")
	if colonIdx == -1 {
		return RedirectRule{}, false
	}

	pathPrefix := rule[:colonIdx]
	target := rule[colonIdx+1:]

	if target == "" {
		return RedirectRule{}, false
	}

	// Parse target as host:port/path
	// Find the slash that separates host:port from path (if any)
	var targetHost, targetPath string

	slashIdx := strings.Index(target, "/")
	if slashIdx == -1 {
		// No path in target, e.g., "localhost:5173"
		targetHost = target
		targetPath = pathPrefix // Default to same path
	} else {
		// Has path, e.g., "localhost:5173/app"
		targetHost = target[:slashIdx]
		targetPath = target[slashIdx:]
	}

	if targetHost == "" {
		return RedirectRule{}, false
	}

	return RedirectRule{
		PathPrefix: pathPrefix,
		TargetHost: targetHost,
		TargetPath: targetPath,
	}, true
}

// ParseLogLevel parses a log level string into slog.Level.
// Valid values: debug, info, warn, error (case-insensitive).
// Returns slog.LevelInfo if the value is empty or invalid.
func ParseLogLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo // Default to info level
	}
}
