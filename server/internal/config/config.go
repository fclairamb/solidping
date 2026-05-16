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
	Node        NodeConfig           `koanf:"node"`
	Profiler    ProfilerConfig       `koanf:"profiler"`
	OTel        OTelConfig           `koanf:"otel"`
	Sentry      SentryConfig         `koanf:"sentry"`
	Prometheus  PrometheusConfig     `koanf:"prometheus"`
	Checkers    CheckersConfig       `koanf:"checkers"`
	Aggregation AggregationConfig    `koanf:"aggregation"`
	FileStorage FileStorageConfig    `koanf:"filestorage"`
	App         AppConfig            `koanf:"app"`
	Deployment  DeploymentConfig     `koanf:"deployment"`
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
// lives in the `files` table. AWS credentials are not stored here — they
// come from the standard AWS SDK chain (env, IAM role, shared config).
type FileStorageConfig struct {
	Type      string `koanf:"type"`       // "local" (default) or "s3"
	LocalRoot string `koanf:"local_root"` // local backend root, e.g. "./data/files"
	S3Bucket  string `koanf:"s3_bucket"`  // S3 backend bucket name
	S3Region  string `koanf:"s3_region"`  // S3 backend region
	S3Prefix  string `koanf:"s3_prefix"`  // optional key prefix
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
	JWTSecret                string         `koanf:"jwt_secret"`
	AccessTokenExpiry        time.Duration  `koanf:"access_token_expiry"`
	RefreshTokenExpiry       time.Duration  `koanf:"refresh_token_expiry"`
	RegistrationEmailPattern string         `koanf:"registration_email_pattern"`
	WebAuthn                 WebAuthnConfig `koanf:"webauthn"`
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
}

// JobWorkerConfig contains job worker configuration.
type JobWorkerConfig struct {
	FetchMaxAhead time.Duration `koanf:"fetch_max_ahead"` // Max time ahead to look for jobs
	Nb            int           `koanf:"nb"`              // Max concurrent goroutines
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
}

// ServerConfig contains HTTP server configuration.
type ServerConfig struct {
	Listen          string            `koanf:"listen"`
	BaseURL         string            `koanf:"base_url"`     // Public URL where SolidPing is accessible
	JobWorker       JobWorkerConfig   `koanf:"job_worker"`   // TODO: Move it to Config
	CheckWorker     CheckWorkerConfig `koanf:"check_worker"` // TODO: Move it to Config
	ShutdownTimeout time.Duration     `koanf:"shutdown_timeout"`
	RateLimiting    RateLimitConfig   `koanf:"rate_limiting"` // Per-IP HTTP rate and concurrency limits
	Redirects       []RedirectRule    `koanf:"-"`             // Parsed from SP_REDIRECTS env var
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
}

// Load reads configuration from defaults, config file, and environment variables.
//
//nolint:funlen,cyclop // Configuration loading requires setting many defaults and has multiple branches
func Load() (*Config, error) {
	koanfInstance := koanf.New(".")

	// Set defaults
	defaults := Config{
		Server: ServerConfig{
			Listen:          ":4000",
			BaseURL:         "http://localhost:4000",
			ShutdownTimeout: 30 * time.Second,
			JobWorker: JobWorkerConfig{
				FetchMaxAhead: 5 * time.Minute,
				Nb:            2,
			},
			CheckWorker: CheckWorkerConfig{
				FetchMaxAhead: 5 * time.Minute,
				Nb:            3,
			},
			RateLimiting: RateLimitConfig{
				RequestsPerMinute: 300,
				Burst:             60,
				MaxConcurrent:     20,
				TrustedProxies:    0,
			},
		},
		Database: DatabaseConfig{
			Type: DatabaseTypeSQLite,
			Dir:  ".",
		},
		Auth: AuthConfig{
			JWTSecret:          "change-me-in-production",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
			WebAuthn: WebAuthnConfig{
				Enabled:       true,
				RPDisplayName: "SolidPing",
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
		Node: NodeConfig{
			Role:   NodeRoleAll,
			Region: "",
		},
		Profiler: ProfilerConfig{
			Enabled: false,
			Listen:  "localhost:6060",
		},
		Prometheus: PrometheusConfig{
			Enabled: true,
			Path:    "/metrics",
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

	applyRateLimitingEnv(&cfg.Server.RateLimiting)

	// When in test mode and no database type is specified, default to sqlite-memory
	if cfg.RunMode == "test" && cfg.Database.Type == "" {
		cfg.Database.Type = DatabaseTypeSQLiteMemory
	}

	// Manually read SP_DB_RESET for database reset on startup
	if dbReset := os.Getenv("SP_DB_RESET"); dbReset == "true" || dbReset == "1" {
		cfg.Database.Reset = true
	}

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

// applyRateLimitingEnv reads SP_SERVER_RATE_LIMITING_* into rl. The koanf env
// loader collapses every underscore in SP_*-prefixed names to a dot, so it
// would map these to server.rate.limiting.* and miss the snake_case koanf tags
// (rate_limiting, requests_per_minute, max_concurrent, trusted_proxies).
func applyRateLimitingEnv(rl *RateLimitConfig) {
	intEnv := func(name string, dst *int) {
		v := os.Getenv(name)
		if v == "" {
			return
		}
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
	}
	intEnv("SP_SERVER_RATE_LIMITING_REQUESTS_PER_MINUTE", &rl.RequestsPerMinute)
	intEnv("SP_SERVER_RATE_LIMITING_BURST", &rl.Burst)
	intEnv("SP_SERVER_RATE_LIMITING_MAX_CONCURRENT", &rl.MaxConcurrent)
	intEnv("SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES", &rl.TrustedProxies)
}

// ComputeBugReportEnabled returns true iff a GitHub PAT and repo are configured.
// Used at startup and after a system-parameter reload of app.github.* keys.
func ComputeBugReportEnabled(gh *AppGitHubConfig) bool {
	return gh.IssuesToken != "" && gh.Repo != ""
}

// Validate checks that the configuration is valid and returns an error if not.
func (c *Config) Validate() error {
	// Validate database type
	validTypes := []string{
		DatabaseTypePostgres,
		DatabaseTypePostgresEmbedded,
		DatabaseTypeSQLite,
		DatabaseTypeSQLiteMemory,
	}

	if !slices.Contains(validTypes, c.Database.Type) {
		return fmt.Errorf("%w, got '%s'", ErrInvalidDatabaseType, c.Database.Type)
	}

	// Validate postgres requires URL
	if c.Database.Type == DatabaseTypePostgres && c.Database.URL == "" {
		return ErrDatabaseURLRequired
	}

	// Validate sqlite requires directory (unless memory mode or test mode)
	if c.Database.Type == DatabaseTypeSQLite && c.Database.Dir == "" {
		return ErrDatabaseDirRequired
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
