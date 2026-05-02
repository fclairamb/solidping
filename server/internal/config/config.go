// Package config provides application configuration management using koanf.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sort"
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
	Server     ServerConfig         `koanf:"server"`
	Database   DatabaseConfig       `koanf:"db"`
	Auth       AuthConfig           `koanf:"auth"`
	Email      EmailConfig          `koanf:"email"`
	Slack      SlackConfig          `koanf:"slack"`
	Google     GoogleOAuthConfig    `koanf:"google"`
	GitHub     GitHubOAuthConfig    `koanf:"github"`
	Microsoft  MicrosoftOAuthConfig `koanf:"microsoft"`
	GitLab     GitLabOAuthConfig    `koanf:"gitlab"`
	Discord    DiscordOAuthConfig   `koanf:"discord"`
	Node       NodeConfig           `koanf:"node"`
	Profiler   ProfilerConfig       `koanf:"profiler"`
	OTel       OTelConfig           `koanf:"otel"`
	Sentry     SentryConfig         `koanf:"sentry"`
	Prometheus PrometheusConfig     `koanf:"prometheus"`
	Checkers   CheckersConfig       `koanf:"checkers"`
	RunMode    string               `koanf:"runmode"`   // "test" for test mode, empty for normal mode
	UserAgent  string               `koanf:"useragent"` // Identity string for protocol checks (SP_USERAGENT)
	LogLevel   slog.Level           `koanf:"-"`         // Logging level (parsed from LOG_LEVEL env var)
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

// AuthConfig contains authentication configuration.
type AuthConfig struct {
	JWTSecret                string        `koanf:"jwt_secret"`
	AccessTokenExpiry        time.Duration `koanf:"access_token_expiry"`
	RefreshTokenExpiry       time.Duration `koanf:"refresh_token_expiry"`
	RegistrationEmailPattern string        `koanf:"registration_email_pattern"`
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

// ServerConfig contains HTTP server configuration.
type ServerConfig struct {
	Listen          string            `koanf:"listen"`
	BaseURL         string            `koanf:"base_url"`     // Public URL where SolidPing is accessible
	JobWorker       JobWorkerConfig   `koanf:"job_worker"`   // TODO: Move it to Config
	CheckWorker     CheckWorkerConfig `koanf:"check_worker"` // TODO: Move it to Config
	ShutdownTimeout time.Duration     `koanf:"shutdown_timeout"`
	Redirects       []RedirectRule    `koanf:"-"` // Parsed from SP_REDIRECTS env var
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
		},
		Database: DatabaseConfig{
			Type: DatabaseTypeSQLite,
			Dir:  ".",
		},
		Auth: AuthConfig{
			JWTSecret:          "change-me-in-production",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
		Email: EmailConfig{
			Port:     587,
			Protocol: "starttls",
			Enabled:  false,
		},
		Google:    GoogleOAuthConfig{Enabled: true},
		GitHub:    GitHubOAuthConfig{Enabled: true},
		GitLab:    GitLabOAuthConfig{Enabled: true},
		Microsoft: MicrosoftOAuthConfig{Enabled: true},
		Slack:     SlackConfig{Enabled: true},
		Discord:   DiscordOAuthConfig{Enabled: true},
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

	return &cfg, nil
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
