// Package systemconfig manages system configuration with precedence:
// 1. Environment variables (SP_*) - highest priority
// 2. Database parameters (organization_uid IS NULL)
// 3. Hardcoded defaults - lowest priority
package systemconfig

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
)

// ParameterKey represents a known system parameter key.
type ParameterKey string

// Known system parameter keys.
const (
	KeyJWTSecret                ParameterKey = "jwt_secret"
	KeyJobWorkers               ParameterKey = "job_workers"
	KeyCheckWorkers             ParameterKey = "check_workers"
	KeyBaseURL                  ParameterKey = "base_url"
	KeyNodeRole                 ParameterKey = "node_role"
	KeyNodeRegion               ParameterKey = "node_region"
	KeyEmailHost                ParameterKey = "email.host"
	KeyEmailPort                ParameterKey = "email.port"
	KeyEmailUsername            ParameterKey = "email.username"
	KeyEmailPassword            ParameterKey = "email.password"
	KeyEmailFrom                ParameterKey = "email.from"
	KeyEmailFromName            ParameterKey = "email.from_name"
	KeyEmailEnabled             ParameterKey = "email.enabled"
	KeyEmailAuthType            ParameterKey = "email.auth_type"
	KeyEmailInsecure            ParameterKey = "email.insecure_skip_verify"
	KeyRegistrationEmailPattern ParameterKey = "auth.registration_email_pattern"
	KeyEmailProtocol            ParameterKey = "email.protocol"

	KeyGoogleClientID           ParameterKey = "auth.google.client_id"
	KeyGoogleClientSecret       ParameterKey = "auth.google.client_secret"
	KeyGitHubClientID           ParameterKey = "auth.github.client_id"
	KeyGitHubClientSecret       ParameterKey = "auth.github.client_secret"
	KeyGitLabClientID           ParameterKey = "auth.gitlab.client_id"
	KeyGitLabClientSecret       ParameterKey = "auth.gitlab.client_secret"
	KeyMicrosoftClientID        ParameterKey = "auth.microsoft.client_id"
	KeyMicrosoftClientSecret    ParameterKey = "auth.microsoft.client_secret"
	KeySlackAppID               ParameterKey = "auth.slack.app_id"
	KeySlackClientID            ParameterKey = "auth.slack.client_id"
	KeySlackClientSecret        ParameterKey = "auth.slack.client_secret"
	KeySlackSigningSecret       ParameterKey = "auth.slack.signing_secret"
	KeyDiscordClientID          ParameterKey = "auth.discord.client_id"
	KeyDiscordClientSecret      ParameterKey = "auth.discord.client_secret"
	KeyDiscordBotToken          ParameterKey = "auth.discord.bot_token"
	KeyDiscordRedirectURL       ParameterKey = "auth.discord.redirect_url"
	KeyGoogleEnabled            ParameterKey = "auth.google.enabled"
	KeyGitHubEnabled            ParameterKey = "auth.github.enabled"
	KeyGitLabEnabled            ParameterKey = "auth.gitlab.enabled"
	KeyMicrosoftEnabled         ParameterKey = "auth.microsoft.enabled"
	KeySlackEnabled             ParameterKey = "auth.slack.enabled"
	KeyDiscordEnabled           ParameterKey = "auth.discord.enabled"
	KeyAggregationRetentionRaw  ParameterKey = "aggregation.retention_raw"
	KeyAggregationRetentionHour ParameterKey = "aggregation.retention_hour"
	KeyAggregationRetentionDay  ParameterKey = "aggregation.retention_day"
)

// ParameterDefinition defines a system parameter with its env var mapping.
type ParameterDefinition struct {
	Key       ParameterKey
	EnvVar    string
	Secret    bool
	ApplyFunc func(cfg *config.Config, value any)
}

// knownParameters defines all known system parameters.
//
//nolint:cyclop,funlen,gocognit // This is a data definition function, not complex logic
func getKnownParameters() []ParameterDefinition {
	return []ParameterDefinition{
		{
			Key:    KeyJWTSecret,
			EnvVar: "SP_AUTH_JWT_SECRET",
			Secret: true,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok && v != "" {
					cfg.Auth.JWTSecret = v
				}
			},
		},
		{
			Key:    KeyJobWorkers,
			EnvVar: "SP_SERVER_JOB_WORKER_NB",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(float64); ok {
					cfg.Server.JobWorker.Nb = int(v)
				} else if v, ok := value.(int); ok {
					cfg.Server.JobWorker.Nb = v
				}
			},
		},
		{
			Key:    KeyCheckWorkers,
			EnvVar: "SP_SERVER_CHECK_WORKER_NB",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(float64); ok {
					cfg.Server.CheckWorker.Nb = int(v)
				} else if v, ok := value.(int); ok {
					cfg.Server.CheckWorker.Nb = v
				}
			},
		},
		{
			Key:    KeyBaseURL,
			EnvVar: "SP_BASE_URL",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok && v != "" {
					cfg.Server.BaseURL = v
				}
			},
		},
		{
			Key:    KeyNodeRole,
			EnvVar: "SP_NODE_ROLE",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok && v != "" {
					cfg.Node.Role = v
				}
			},
		},
		{
			Key:    KeyNodeRegion,
			EnvVar: "SP_NODE_REGION",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok && v != "" {
					cfg.Node.Region = v
					// Also set the check worker region if not already set
					if cfg.Server.CheckWorker.Region == "" {
						cfg.Server.CheckWorker.Region = v
					}
				}
			},
		},
		{
			Key:    KeyEmailHost,
			EnvVar: "SP_EMAIL_HOST",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Email.Host = v
				}
			},
		},
		{
			Key:    KeyEmailPort,
			EnvVar: "SP_EMAIL_PORT",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(float64); ok {
					cfg.Email.Port = int(v)
				} else if v, ok := value.(int); ok {
					cfg.Email.Port = v
				}
			},
		},
		{
			Key:    KeyEmailUsername,
			EnvVar: "SP_EMAIL_USERNAME",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Email.Username = v
				}
			},
		},
		{
			Key:    KeyEmailPassword,
			EnvVar: "SP_EMAIL_PASSWORD",
			Secret: true,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Email.Password = v
				}
			},
		},
		{
			Key:    KeyEmailFrom,
			EnvVar: "SP_EMAIL_FROM",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Email.From = v
				}
			},
		},
		{
			Key:    KeyEmailFromName,
			EnvVar: "SP_EMAIL_FROMNAME",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Email.FromName = v
				}
			},
		},
		{
			Key:    KeyEmailEnabled,
			EnvVar: "SP_EMAIL_ENABLED",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(bool); ok {
					cfg.Email.Enabled = v
				}
			},
		},
		{
			Key:    KeyEmailAuthType,
			EnvVar: "SP_EMAIL_AUTHTYPE",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Email.AuthType = v
				}
			},
		},
		{
			Key:    KeyEmailInsecure,
			EnvVar: "SP_EMAIL_INSECURESKIPVERIFY",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(bool); ok {
					cfg.Email.InsecureSkipVerify = v
				}
			},
		},
		{
			Key:    KeyRegistrationEmailPattern,
			EnvVar: "SP_AUTH_REGISTRATION_EMAIL_PATTERN",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Auth.RegistrationEmailPattern = v
				}
			},
		},
		{
			Key:    KeyEmailProtocol,
			EnvVar: "SP_EMAIL_PROTOCOL",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Email.Protocol = v
				}
			},
		},
		{
			Key:    KeyGoogleClientID,
			EnvVar: "SP_GOOGLE_CLIENT_ID",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Google.ClientID = v
				}
			},
		},
		{
			Key:    KeyGoogleClientSecret,
			EnvVar: "SP_GOOGLE_CLIENT_SECRET",
			Secret: true,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Google.ClientSecret = v
				}
			},
		},
		{
			Key:    KeyGitHubClientID,
			EnvVar: "SP_GITHUB_CLIENT_ID",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.GitHub.ClientID = v
				}
			},
		},
		{
			Key:    KeyGitHubClientSecret,
			EnvVar: "SP_GITHUB_CLIENT_SECRET",
			Secret: true,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.GitHub.ClientSecret = v
				}
			},
		},
		{
			Key:    KeyGitLabClientID,
			EnvVar: "SP_GITLAB_CLIENT_ID",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.GitLab.ClientID = v
				}
			},
		},
		{
			Key:    KeyGitLabClientSecret,
			EnvVar: "SP_GITLAB_CLIENT_SECRET",
			Secret: true,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.GitLab.ClientSecret = v
				}
			},
		},
		{
			Key:    KeyMicrosoftClientID,
			EnvVar: "SP_MICROSOFT_CLIENT_ID",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Microsoft.ClientID = v
				}
			},
		},
		{
			Key:    KeyMicrosoftClientSecret,
			EnvVar: "SP_MICROSOFT_CLIENT_SECRET",
			Secret: true,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Microsoft.ClientSecret = v
				}
			},
		},
		{
			Key:    KeySlackAppID,
			EnvVar: "SP_SLACK_APP_ID",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Slack.AppID = v
				}
			},
		},
		{
			Key:    KeySlackClientID,
			EnvVar: "SP_SLACK_CLIENT_ID",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Slack.ClientID = v
				}
			},
		},
		{
			Key:    KeySlackClientSecret,
			EnvVar: "SP_SLACK_CLIENT_SECRET",
			Secret: true,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Slack.ClientSecret = v
				}
			},
		},
		{
			Key:    KeySlackSigningSecret,
			EnvVar: "SP_SLACK_SIGNING_SECRET",
			Secret: true,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Slack.SigningSecret = v
				}
			},
		},
		{
			Key:    KeyDiscordClientID,
			EnvVar: "SP_DISCORD_CLIENT_ID",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Discord.ClientID = v
				}
			},
		},
		{
			Key:    KeyDiscordClientSecret,
			EnvVar: "SP_DISCORD_CLIENT_SECRET",
			Secret: true,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Discord.ClientSecret = v
				}
			},
		},
		{
			Key:    KeyDiscordBotToken,
			EnvVar: "SP_DISCORD_BOT_TOKEN",
			Secret: true,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Discord.BotToken = v
				}
			},
		},
		{
			Key:    KeyDiscordRedirectURL,
			EnvVar: "SP_DISCORD_REDIRECT_URL",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Discord.RedirectURL = v
				}
			},
		},
		{
			Key:    KeyGoogleEnabled,
			EnvVar: "SP_GOOGLE_ENABLED",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				cfg.Google.Enabled = parseBool(value, cfg.Google.Enabled)
			},
		},
		{
			Key:    KeyGitHubEnabled,
			EnvVar: "SP_GITHUB_ENABLED",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				cfg.GitHub.Enabled = parseBool(value, cfg.GitHub.Enabled)
			},
		},
		{
			Key:    KeyGitLabEnabled,
			EnvVar: "SP_GITLAB_ENABLED",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				cfg.GitLab.Enabled = parseBool(value, cfg.GitLab.Enabled)
			},
		},
		{
			Key:    KeyMicrosoftEnabled,
			EnvVar: "SP_MICROSOFT_ENABLED",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				cfg.Microsoft.Enabled = parseBool(value, cfg.Microsoft.Enabled)
			},
		},
		{
			Key:    KeySlackEnabled,
			EnvVar: "SP_SLACK_ENABLED",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				cfg.Slack.Enabled = parseBool(value, cfg.Slack.Enabled)
			},
		},
		{
			Key:    KeyDiscordEnabled,
			EnvVar: "SP_DISCORD_ENABLED",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				cfg.Discord.Enabled = parseBool(value, cfg.Discord.Enabled)
			},
		},
		{
			Key:    KeyAggregationRetentionRaw,
			EnvVar: "SP_AGGREGATION_RETENTION_RAW",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := parseInt(value); ok {
					cfg.Aggregation.RetentionRaw = v
				}
			},
		},
		{
			Key:    KeyAggregationRetentionHour,
			EnvVar: "SP_AGGREGATION_RETENTION_HOUR",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := parseInt(value); ok {
					cfg.Aggregation.RetentionHour = v
				}
			},
		},
		{
			Key:    KeyAggregationRetentionDay,
			EnvVar: "SP_AGGREGATION_RETENTION_DAY",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := parseInt(value); ok {
					cfg.Aggregation.RetentionDay = v
				}
			},
		},
	}
}

// parseInt coerces a config value to int. Accepts native int / float64 / numeric
// string. Returns ok=false on any other input so the caller can keep its default.
func parseInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case float64:
		return int(typed), true
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%d", &n); err == nil {
			return n, true
		}
	}

	return 0, false
}

const (
	boolStringTrue = "true"
	boolStringYes  = "yes"
	boolStringOne  = "1"
)

// parseBool coerces a config value to bool. Accepts native bool, the strings
// "true"/"false"/"1"/"0"/"yes"/"no" (case-insensitive), and falls back to
// defaultValue on anything else (including empty string).
func parseBool(value any, defaultValue bool) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case boolStringTrue, boolStringOne, boolStringYes:
			return true
		case "false", "0", "no":
			return false
		default:
			return defaultValue
		}
	default:
		return defaultValue
	}
}

// Service manages system configuration.
type Service struct {
	db     db.Service
	config *config.Config
}

// NewService creates a new system config service.
func NewService(dbService db.Service, cfg *config.Config) *Service {
	return &Service{
		db:     dbService,
		config: cfg,
	}
}

// Initialize loads system parameters from the database and applies them to the config.
// It also auto-generates the JWT secret if not set.
func (s *Service) Initialize(ctx context.Context) error {
	// Load all system parameters from database
	params, err := s.db.ListSystemParameters(ctx)
	if err != nil {
		return fmt.Errorf("failed to load system parameters: %w", err)
	}

	// Build a map for quick lookup
	paramMap := make(map[string]any)
	for _, p := range params {
		if val, ok := p.Value["value"]; ok {
			paramMap[p.Key] = val
		}
	}

	knownParameters := getKnownParameters()

	// Apply parameters with precedence: env > db > defaults
	for i := range knownParameters {
		def := &knownParameters[i]
		// Check environment variable first (highest priority)
		if envVal := os.Getenv(def.EnvVar); envVal != "" {
			def.ApplyFunc(s.config, envVal)

			continue
		}

		// Check database value
		if dbVal, ok := paramMap[string(def.Key)]; ok {
			def.ApplyFunc(s.config, dbVal)
		}
	}

	// Auto-generate JWT secret if needed
	if err := s.ensureJWTSecret(ctx); err != nil {
		return fmt.Errorf("failed to ensure JWT secret: %w", err)
	}

	return nil
}

// ensureJWTSecret checks if JWT secret is set and generates one if missing.
func (s *Service) ensureJWTSecret(ctx context.Context) error {
	// Check if already set in environment variable
	if os.Getenv("SP_AUTH_JWT_SECRET") != "" {
		return nil
	}

	// Check if set to something other than the default
	if s.config.Auth.JWTSecret != "" && s.config.Auth.JWTSecret != "change-me-in-production" {
		return nil
	}

	// Check if set in database
	param, err := s.db.GetSystemParameter(ctx, string(KeyJWTSecret))
	if err != nil {
		return err
	}

	if param != nil {
		if val, ok := param.Value["value"].(string); ok && val != "" {
			s.config.Auth.JWTSecret = val

			return nil
		}
	}

	// Auto-generate a secure secret
	secret, err := generateSecureSecret(32)
	if err != nil {
		return fmt.Errorf("failed to generate JWT secret: %w", err)
	}

	// Save to database
	if err := s.db.SetSystemParameter(ctx, string(KeyJWTSecret), secret, true); err != nil {
		return fmt.Errorf("failed to save JWT secret: %w", err)
	}

	s.config.Auth.JWTSecret = secret
	slog.WarnContext(ctx, "JWT secret auto-generated and saved to database")

	return nil
}

// generateSecureSecret generates a cryptographically secure random string.
func generateSecureSecret(length int) (string, error) {
	bytes := make([]byte, length)

	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(bytes), nil
}

// GetString retrieves a system parameter as a string.
// Checks environment variable first, then database, then returns default.
func (s *Service) GetString(ctx context.Context, key ParameterKey, envVar string, defaultValue string) (string, error) {
	// Check environment variable first
	if envVal := os.Getenv(envVar); envVal != "" {
		return envVal, nil
	}

	// Check database
	param, err := s.db.GetSystemParameter(ctx, string(key))
	if err != nil {
		return defaultValue, err
	}

	if param != nil {
		if val, ok := param.Value["value"].(string); ok {
			return val, nil
		}
	}

	return defaultValue, nil
}

// GetInt retrieves a system parameter as an int.
func (s *Service) GetInt(ctx context.Context, key ParameterKey, envVar string, defaultValue int) (int, error) {
	// Check environment variable first
	if envVal := os.Getenv(envVar); envVal != "" {
		var val int
		if _, err := fmt.Sscanf(envVal, "%d", &val); err == nil {
			return val, nil
		}
	}

	// Check database
	param, err := s.db.GetSystemParameter(ctx, string(key))
	if err != nil {
		return defaultValue, err
	}

	if param != nil {
		if val, ok := param.Value["value"].(float64); ok {
			return int(val), nil
		}

		if val, ok := param.Value["value"].(int); ok {
			return val, nil
		}
	}

	return defaultValue, nil
}

// GetBool retrieves a system parameter as a bool.
func (s *Service) GetBool(ctx context.Context, key ParameterKey, envVar string, defaultValue bool) (bool, error) {
	// Check environment variable first
	if envVal := os.Getenv(envVar); envVal != "" {
		return envVal == "true" || envVal == "1" || envVal == "yes", nil
	}

	// Check database
	param, err := s.db.GetSystemParameter(ctx, string(key))
	if err != nil {
		return defaultValue, err
	}

	if param != nil {
		if val, ok := param.Value["value"].(bool); ok {
			return val, nil
		}
	}

	return defaultValue, nil
}
