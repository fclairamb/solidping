// Package config provides application configuration management using koanf.
package config

import (
	"errors"
	"fmt"
	"log/slog"
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
)

// Node role constants.
const (
	NodeRoleAll    = "all"
	NodeRoleAPI    = "api"
	NodeRoleJobs   = "jobs"
	NodeRoleChecks = "checks"
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
	// ErrInvalidNodeRole is returned when the node role is invalid.
	ErrInvalidNodeRole = errors.New("node role must be 'all', 'api', 'jobs', or 'checks'")
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

// ValidNodeRoles returns all valid role values.
func ValidNodeRoles() []string {
	return []string{NodeRoleAll, NodeRoleAPI, NodeRoleJobs, NodeRoleChecks}
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
	// AuthGrace is how long an unauthenticated connection has to send an
	// `auth` message before the server closes it with 4401
	// (SP_REALTIME_AUTH_GRACE, default 5s). Only applies to connections that
	// didn't pre-authenticate via header/cookie at upgrade time.
	AuthGrace time.Duration `koanf:"auth_grace"`
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
}

// SentryConfig contains Sentry error tracking configuration.
type SentryConfig struct {
	DSN              string  `koanf:"dsn"`                // Sentry DSN (empty = disabled)
	Environment      string  `koanf:"environment"`        // development, staging, production
	TracesSampleRate float64 `koanf:"traces_sample_rate"` // 0.0 to 1.0 (default 0.1)
	Debug            bool    `koanf:"debug"`              // Enable Sentry debug logging
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
	Server      ServerConfig         `koanf:"server"`
	Database    DatabaseConfig       `koanf:"db"`
	Auth        AuthConfig           `koanf:"auth"`
	Encryption  EncryptionConfig     `koanf:"encryption"`
	Email       EmailConfig          `koanf:"email"`
	Slack       SlackConfig          `koanf:"slack"`
	Google      GoogleOAuthConfig    `koanf:"google"`
	GitHub      GitHubOAuthConfig    `koanf:"github"`
	Microsoft   MicrosoftOAuthConfig `koanf:"microsoft"`
	GitLab      GitLabOAuthConfig    `koanf:"gitlab"`
	Discord     DiscordOAuthConfig   `koanf:"discord"`
	OIDC        OIDCOAuthConfig      `koanf:"oidc"`
	SAML        SAMLConfig           `koanf:"saml"`
	LDAP        LDAPConfig           `koanf:"ldap"`
	Node        NodeConfig           `koanf:"node"`
	Profiler    ProfilerConfig       `koanf:"profiler"`
	Runtime     RuntimeConfig        `koanf:"runtime"`
	OTel        OTelConfig           `koanf:"otel"`
	Sentry      SentryConfig         `koanf:"sentry"`
	Prometheus  PrometheusConfig     `koanf:"prometheus"`
	Realtime    RealtimeConfig       `koanf:"realtime"`
	Checkers    CheckersConfig       `koanf:"checkers"`
	Aggregation AggregationConfig    `koanf:"aggregation"`
	Jobs        JobsConfig           `koanf:"jobs"`
	FileStorage FileStorageConfig    `koanf:"filestorage"`
	App         AppConfig            `koanf:"app"`
	Deployment  DeploymentConfig     `koanf:"deployment"`
	WebPush     WebPushConfig        `koanf:"webpush"`
	RunMode     string               `koanf:"runmode"`   // "test" for test mode, empty for normal mode
	UserAgent   string               `koanf:"useragent"` // Identity string for protocol checks (SP_USERAGENT)
	LogLevel    slog.Level           `koanf:"-"`         // Logging level (parsed from LOG_LEVEL env var)
}

// DeploymentConfig picks per-org entitlement defaults. SP_DEPLOYMENT_MODE
// drives Mode; "self-hosted" (default) caps SSO membership at 30,
// "saas" caps aggregate check executions at 6/min. Validation is at
// startup — unknown values fail fast.
type DeploymentConfig struct {
	Mode string `koanf:"mode"`
}

// NodeConfig contains node role configuration.
type NodeConfig struct {
	Role   string `koanf:"role"`   // Node role: all, api, jobs, checks
	Region string `koanf:"region"` // Node region (required for checks role)
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

// ShouldRunAPI returns true if this node should run the HTTP server.
func (c *Config) ShouldRunAPI() bool {
	return c.Node.Role == NodeRoleAll || c.Node.Role == NodeRoleAPI
}

// ShouldRunJobs returns true if this node should run the job processor.
func (c *Config) ShouldRunJobs() bool {
	return c.Node.Role == NodeRoleAll || c.Node.Role == NodeRoleJobs
}

// ShouldRunChecks returns true if this node should run the check executor.
func (c *Config) ShouldRunChecks() bool {
	return c.Node.Role == NodeRoleAll || c.Node.Role == NodeRoleChecks
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
type AggregationConfig struct {
	RetentionRaw  int `koanf:"retention_raw"`  // hours of raw to keep (default 24)
	RetentionHour int `koanf:"retention_hour"` // days of hourly to keep (default 30)
	RetentionDay  int `koanf:"retention_day"`  // months of daily to keep (default 12)
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
	// Scheduling holds the cost-aware, plan-weighted check-scheduling knobs.
	// Multi-word keys → read via applySchedulingEnv. See project_koanf_env_quirk.
	Scheduling SchedulingConfig `koanf:"scheduling"`
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
	// TierCreditSeconds is the deadline credit per unit of plan_weight (paid
	// jobs sort earlier under contention). 0 disables the credit.
	TierCreditSeconds float64 `koanf:"tier_credit_seconds"`
	// TierCreditMaxSeconds caps total tier credit regardless of weight. 0 = no
	// separate cap.
	TierCreditMaxSeconds float64 `koanf:"tier_credit_max_seconds"`
	// CostTimeoutFactor multiplies cost_ewma_ms to derive the per-check
	// execution timeout, clamped to [floor, 30s]. Default 3 (see Load; on by
	// default per spec 2026-07-01-04 D4). 0 disables (flat 30s timeout). A job
	// that never ran (cost 0) always keeps the full 30s regardless of the floor.
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

	// PostgreSQL connection-pool bounds. Without these, database/sql leaves the
	// pool unbounded (default MaxOpenConns = 0 = unlimited), so a burst can open
	// arbitrarily many connections — each with its own buffers client- and
	// server-side. SQLite ignores these (it is pinned to a single writer).
	MaxOpenConns    int           `koanf:"max_open_conns"`     // 0 = driver default (unlimited)
	MaxIdleConns    int           `koanf:"max_idle_conns"`     // 0 = driver default (2)
	ConnMaxLifetime time.Duration `koanf:"conn_max_lifetime"`  // 0 = no expiry
	ConnMaxIdleTime time.Duration `koanf:"conn_max_idle_time"` // 0 = no reap; idle conns held forever
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
				// Cost-aware timeout is ON by default (spec 2026-07-01-04 D4):
				// timeout = clamp(3 × cost_ewma, 5s, 30s). A 200ms check's
				// worst-case slot occupancy drops 30s → 5s while measured slow
				// checks keep the full ceiling. A never-run job (cost 0) gets
				// the full 30s, not the floor, so first runs measure honestly.
				// Set cost_timeout_factor to 0 to disable.
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
			RateLimiting: RateLimitConfig{
				RequestsPerMinute: 300,
				Burst:             60,
				MaxConcurrent:     20,
				TrustedProxies:    0,
				RateQueue:         10,
				ConcurrencyQueue:  10,
				MaxQueueWait:      30 * time.Second,
			},
		},
		Database: DatabaseConfig{
			Type:            DatabaseTypeSQLite,
			Dir:             ".",
			MaxOpenConns:    dbPoolMaxOpenConnsDefault,
			MaxIdleConns:    dbPoolMaxIdleConnsDefault,
			ConnMaxLifetime: time.Hour,
			ConnMaxIdleTime: 5 * time.Minute,
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
			RetentionHour: 30,
			RetentionDay:  12,
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
		Discord:   DiscordOAuthConfig{Enabled: false},
		OIDC:      OIDCOAuthConfig{Enabled: false},
		SAML:      SAMLConfig{Enabled: false},
		LDAP:      LDAPConfig{Enabled: false},
		Node: NodeConfig{
			Role:   NodeRoleAll,
			Region: "",
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
			AuthGrace:                     5 * time.Second,
			MaxSubscriptionsPerConnection: 512,
		},
		Encryption: EncryptionConfig{
			AutoMigrate: true,
		},
		Deployment: DeploymentConfig{
			Mode: DeploymentModeSelfHosted,
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
			return strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(key, "SP_"), "_", ".")), value
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
	applyAuthEnv(&cfg.Auth)
	applyPasswordHashingEnv(&cfg.Auth.Password)
	applyFileStorageEnv(&cfg.FileStorage)
	applyWebPushEnv(&cfg.WebPush)
	applyJobsEnv(&cfg.Jobs)
	applyServerEnv(&cfg.Server)
	applySchedulingEnv(&cfg.Server.Scheduling)
	applyProfilerEnv(&cfg.Profiler)
	applyRuntimeEnv(&cfg.Runtime)
	applyRealtimeEnv(&cfg.Realtime)

	// When in test mode and no database type is specified, default to sqlite-memory
	if cfg.RunMode == "test" && cfg.Database.Type == "" {
		cfg.Database.Type = DatabaseTypeSQLiteMemory
	}

	// Manually read SP_DB_RESET for database reset on startup
	if dbReset := os.Getenv("SP_DB_RESET"); dbReset == envTrue || dbReset == "1" {
		cfg.Database.Reset = true
	}

	// cfg.Node.Role is only known now (post-unmarshal), so the role-aware pool
	// default must run here — after config.yml/config.local.yml overrides are
	// already folded into cfg.Database, before the SP_DB_* env overrides below.
	applyNodeRolePoolDefaults(&cfg.Database, cfg.Node.Role)

	applyDatabasePoolEnv(&cfg.Database)

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
	intEnv("SP_SERVER_RATE_LIMITING_RATE_QUEUE", &cfg.RateQueue)
	intEnv("SP_SERVER_RATE_LIMITING_CONCURRENCY_QUEUE", &cfg.ConcurrencyQueue)
	durEnv("SP_SERVER_RATE_LIMITING_MAX_QUEUE_WAIT", &cfg.MaxQueueWait)
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
	if v := os.Getenv("SP_REALTIME_AUTH_GRACE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.AuthGrace = d
		}
	}
	if v := os.Getenv("SP_REALTIME_MAX_SUBSCRIPTIONS_PER_CONNECTION"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxSubscriptionsPerConnection = n
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
func applyNodeRolePoolDefaults(cfg *DatabaseConfig, nodeRole string) {
	if nodeRole != NodeRoleChecks && nodeRole != NodeRoleJobs {
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

	// Validate node role
	if !slices.Contains(ValidNodeRoles(), c.Node.Role) {
		return fmt.Errorf("%w, got '%s'", ErrInvalidNodeRole, c.Node.Role)
	}

	// Validate checks role requires region
	if c.Node.Role == NodeRoleChecks && c.Node.Region == "" {
		return ErrRegionRequiredForChecks
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
