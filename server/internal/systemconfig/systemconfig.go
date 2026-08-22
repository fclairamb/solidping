// Package systemconfig manages system configuration with precedence:
// 1. Environment variables (SP_*) - highest priority
// 2. Database parameters (organization_uid IS NULL)
// 3. Hardcoded defaults - lowest priority
package systemconfig

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
)

// ParameterKey represents a known system parameter key.
type ParameterKey string

// Known system parameter keys.
const (
	KeyJWTSecret                ParameterKey = "auth.jwt_secret"
	KeyJobWorkers               ParameterKey = "server.job_workers"
	KeyCheckWorkers             ParameterKey = "server.check_workers"
	KeyBaseURL                  ParameterKey = "server.base_url"
	KeyNodeRole                 ParameterKey = "node.role"
	KeyNodeRegion               ParameterKey = "node.region"
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

	// KeySessionMaxDuration is the global (system-wide) hard cap on session
	// lifetime in seconds, measured from login (spec
	// 2026-07-08-01-per-org-session-duration-and-longer-token-refresh). An
	// org may override it with a parameter row of the same key
	// (organization_uid set) — see handlers/auth.Service.resolveSessionMaxDuration,
	// which resolves org parameter → this system value → 0 (unlimited).
	// Unset/zero means unlimited, matching today's behavior (only the
	// sliding RefreshTokenExpiry idle window applies).
	KeySessionMaxDuration ParameterKey = "auth.session_max_duration"

	KeyGoogleClientID         ParameterKey = "auth.google.client_id"
	KeyGoogleClientSecret     ParameterKey = "auth.google.client_secret"
	KeyGitHubClientID         ParameterKey = "auth.github.client_id"
	KeyGitHubClientSecret     ParameterKey = "auth.github.client_secret"
	KeyGitLabClientID         ParameterKey = "auth.gitlab.client_id"
	KeyGitLabClientSecret     ParameterKey = "auth.gitlab.client_secret"
	KeyMicrosoftClientID      ParameterKey = "auth.microsoft.client_id"
	KeyMicrosoftClientSecret  ParameterKey = "auth.microsoft.client_secret"
	KeyMicrosoftTenantID      ParameterKey = "auth.microsoft.tenant_id"
	KeySlackAppID             ParameterKey = "auth.slack.app_id"
	KeySlackClientID          ParameterKey = "auth.slack.client_id"
	KeySlackClientSecret      ParameterKey = "auth.slack.client_secret"
	KeySlackSigningSecret     ParameterKey = "auth.slack.signing_secret"
	KeySlackSocketModeEnabled ParameterKey = "auth.slack.socket_mode_enabled"
	KeySlackAppToken          ParameterKey = "auth.slack.app_token"
	// Microsoft Teams bot (Azure Bot / Bot Framework) keys. Unlike the Slack
	// keys these are NOT under auth.* — the Teams bot is purely an
	// integration, never an authentication provider — so they mirror the
	// config struct path msteams.* per the parameter-key convention.
	KeyMSTeamsEnabled           ParameterKey = "msteams.enabled"
	KeyMSTeamsAppID             ParameterKey = "msteams.app_id"
	KeyMSTeamsAppSecret         ParameterKey = "msteams.app_secret"
	KeyMSTeamsTenantID          ParameterKey = "msteams.tenant_id"
	KeyDiscordClientID          ParameterKey = "auth.discord.client_id"
	KeyDiscordClientSecret      ParameterKey = "auth.discord.client_secret"
	KeyDiscordBotToken          ParameterKey = "auth.discord.bot_token"
	KeyDiscordRedirectURL       ParameterKey = "auth.discord.redirect_url"
	KeyDiscordPublicKey         ParameterKey = "auth.discord.public_key"
	KeyDiscordGatewayEnabled    ParameterKey = "auth.discord.gateway_enabled"
	KeyGoogleEnabled            ParameterKey = "auth.google.enabled"
	KeyGitHubEnabled            ParameterKey = "auth.github.enabled"
	KeyGitLabEnabled            ParameterKey = "auth.gitlab.enabled"
	KeyMicrosoftEnabled         ParameterKey = "auth.microsoft.enabled"
	KeySlackEnabled             ParameterKey = "auth.slack.enabled"
	KeyDiscordEnabled           ParameterKey = "auth.discord.enabled"
	KeyAggregationRetentionRaw  ParameterKey = "aggregation.retention_raw"
	KeyAggregationRetentionHour ParameterKey = "aggregation.retention_hour"
	KeyAggregationRetentionDay  ParameterKey = "aggregation.retention_day"

	// Global performance / data-retention knobs, resolved at job-run time
	// through the env → DB parameter → default precedence (spec 2026-07-11-16
	// §4; the wider performance.* group is spec 2026-07-11-17). These replace
	// the koanf-only aggregation.retention_* fields for aggregation
	// work-discovery — the unit suffix keeps the integer values unambiguous.
	KeyPerfAggRetentionRawHours  ParameterKey = "performance.aggregation_retention_raw_hours"
	KeyPerfAggRetentionHourDays  ParameterKey = "performance.aggregation_retention_hour_days"
	KeyPerfAggRetentionDayMonths ParameterKey = "performance.aggregation_retention_day_months"

	// Jobs-table retention knobs (spec 2026-07-11-17). Resolved at job-run time
	// by the same env → global DB parameter → default precedence as the
	// aggregation perf keys (jobtypes.resolveRetentionTier). A finished job is
	// soft-deleted after KeyPerfJobsSoftDeleteHours (stage 1) and hard-deleted
	// KeyPerfJobsHardDeleteHours after that (stage 2). Defaults 48 / 24.
	KeyPerfJobsSoftDeleteHours ParameterKey = "performance.jobs_soft_delete_hours"
	KeyPerfJobsHardDeleteHours ParameterKey = "performance.jobs_hard_delete_hours"

	// KeyAuditRetentionDays is how many days the security/configuration audit
	// trail is kept (spec 2026-08-21-09). Resolved at job-run time by the same
	// env → global DB parameter → koanf → default precedence as the perf keys
	// above (jobtypes.resolveRetentionTier). Default 365; 0 disables the sweep.
	KeyAuditRetentionDays ParameterKey = "audit.retention_days"

	// Generic OAuth2/OIDC provider keys (spec 2026-07-08-08, part 1). Global-only,
	// single instance — see config.OIDCOAuthConfig.
	KeyOIDCEnabled      ParameterKey = "auth.oidc.enabled"
	KeyOIDCDisplayName  ParameterKey = "auth.oidc.display_name"
	KeyOIDCIssuerURL    ParameterKey = "auth.oidc.issuer_url"
	KeyOIDCClientID     ParameterKey = "auth.oidc.client_id"
	KeyOIDCClientSecret ParameterKey = "auth.oidc.client_secret"
	KeyOIDCScopes       ParameterKey = "auth.oidc.scopes"
	KeyOIDCEmailClaim   ParameterKey = "auth.oidc.email_claim"
	KeyOIDCNameClaim    ParameterKey = "auth.oidc.name_claim"
	KeyOIDCAvatarClaim  ParameterKey = "auth.oidc.avatar_claim"

	// SAML 2.0 Service Provider keys (spec 2026-07-08-08, part 2). Global-only,
	// single instance, SP-initiated only — see config.SAMLConfig. None of these
	// are secrets: SAML trust is certificate-based, not a shared client secret.
	KeySAMLEnabled        ParameterKey = "auth.saml.enabled"
	KeySAMLDisplayName    ParameterKey = "auth.saml.display_name"
	KeySAMLIDPMetadataURL ParameterKey = "auth.saml.idp_metadata_url"
	KeySAMLIDPMetadataXML ParameterKey = "auth.saml.idp_metadata_xml"
	KeySAMLSPEntityID     ParameterKey = "auth.saml.sp_entity_id"
	KeySAMLEmailAttribute ParameterKey = "auth.saml.email_attribute"
	KeySAMLNameAttribute  ParameterKey = "auth.saml.name_attribute"

	// LDAP / Active Directory bind-authentication keys (spec 2026-07-08-08,
	// part 3). Global-only, single connector — see config.LDAPConfig. Only
	// bind_password is a secret: it's the directory service account's
	// credential, analogous to an OAuth client secret.
	KeyLDAPEnabled            ParameterKey = "auth.ldap.enabled"
	KeyLDAPServerURL          ParameterKey = "auth.ldap.server_url"
	KeyLDAPStartTLS           ParameterKey = "auth.ldap.start_tls"
	KeyLDAPInsecureSkipVerify ParameterKey = "auth.ldap.insecure_skip_verify"
	KeyLDAPBindDN             ParameterKey = "auth.ldap.bind_dn"
	KeyLDAPBindPassword       ParameterKey = "auth.ldap.bind_password"
	KeyLDAPBaseDN             ParameterKey = "auth.ldap.base_dn"
	KeyLDAPUserFilter         ParameterKey = "auth.ldap.user_filter"
	KeyLDAPEmailAttribute     ParameterKey = "auth.ldap.email_attribute"
	KeyLDAPNameAttribute      ParameterKey = "auth.ldap.name_attribute"

	// Password-hashing policy keys. These mutate cfg.Auth.Password.* and take
	// effect after a restart, when the policy is re-resolved from the overlaid
	// config (see app.Server.InitializeSystemConfig). Numeric values arrive as
	// float64 from encoding/json; ApplyFunc coerces them.
	KeyPasswordAlgorithm     ParameterKey = "auth.password.algorithm"
	KeyPasswordArgon2Memory  ParameterKey = "auth.password.argon2.memory"
	KeyPasswordArgon2Time    ParameterKey = "auth.password.argon2.time"
	KeyPasswordArgon2Threads ParameterKey = "auth.password.argon2.threads"
	KeyPasswordArgon2KeyLen  ParameterKey = "auth.password.argon2.key_length"
	KeyPasswordArgon2SaltLen ParameterKey = "auth.password.argon2.salt_length"
	KeyPasswordBcryptCost    ParameterKey = "auth.password.bcrypt.cost"
	KeyPasswordRehashOnLogin ParameterKey = "auth.password.rehash_on_login"

	// KeySchedulingFastLaneReserved is the fast-lane floor of the check
	// scheduler's lane reservation (spec 2026-07-01-03 D3): slow-lane checks
	// may never occupy more than pool_size − this many runner slots, so a
	// burst of slow probes cannot starve due fast checks. Applied at startup
	// like the worker pool sizes (the worker clamps it to [0, pool_size−1]).
	KeySchedulingFastLaneReserved ParameterKey = "scheduling.fast_lane_reserved"

	// Product-analytics (PostHog) keys, spec 2026-08-02-08. The feature is
	// entirely inert unless posthog.project_api_key is set: posthog.enabled
	// defaults to true but is only a kill switch — see config.PostHogConfig.Active
	// for the single enablement rule (enabled && project_api_key != "") that the
	// backend, GET /api/v1/config and the dashboard all apply verbatim.
	//
	// Only the personal API key is Secret. The project key is public by design:
	// it is the browser-side ingestion key and is deliberately shipped to the SPA.
	KeyPostHogEnabled        ParameterKey = "posthog.enabled"
	KeyPostHogProjectAPIKey  ParameterKey = "posthog.project_api_key"
	KeyPostHogHost           ParameterKey = "posthog.host"
	KeyPostHogPersonalAPIKey ParameterKey = "posthog.personal_api_key"
)

// SP_* environment variable names for the product-analytics parameters,
// hoisted to constants because the tests assert on them too.
const (
	EnvPostHogEnabled        = "SP_POSTHOG_ENABLED"
	EnvPostHogProjectAPIKey  = "SP_POSTHOG_PROJECT_API_KEY"
	EnvPostHogHost           = "SP_POSTHOG_HOST"
	EnvPostHogPersonalAPIKey = "SP_POSTHOG_PERSONAL_API_KEY"
)

// EnvVarForKey returns the SP_* environment variable bound to a system
// parameter key, and ok=false when the key is not a known parameter. Pure data
// accessor over getKnownParameters — no database dependency.
func EnvVarForKey(key string) (string, bool) {
	params := getKnownParameters()
	for i := range params {
		if string(params[i].Key) == key && params[i].EnvVar != "" {
			return params[i].EnvVar, true
		}
	}

	return "", false
}

// EnvOverriddenKeys returns every known parameter key whose effective value is
// currently forced by its SP_* environment variable, i.e. the keys for which a
// database edit made through the Server Settings UI would appear not to take
// effect (env wins in Service.Initialize). Only key names are returned — never
// values — so it is safe to expose to a super-admin API regardless of secrecy.
func EnvOverriddenKeys() []string {
	params := getKnownParameters()
	out := make([]string, 0, len(params))

	for i := range params {
		if params[i].EnvVar == "" {
			continue
		}

		if os.Getenv(params[i].EnvVar) != "" {
			out = append(out, string(params[i].Key))
		}
	}

	return out
}

// ParameterDefinition defines a system parameter with its env var mapping.
type ParameterDefinition struct {
	Key       ParameterKey
	EnvVar    string
	Secret    bool
	ApplyFunc func(cfg *config.Config, value any)
}

// KnownEnvVars returns the SP_* environment variable name of every system
// parameter in the parameter table (its EnvVar field). It is a pure data
// accessor over getKnownParameters with no database dependency, so the startup
// unrecognized-env check can call it before the systemconfig service is
// initialized. Definitions with an empty EnvVar (none today) are skipped.
func KnownEnvVars() []string {
	params := getKnownParameters()
	out := make([]string, 0, len(params))
	for i := range params {
		if params[i].EnvVar != "" {
			out = append(out, params[i].EnvVar)
		}
	}

	return out
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
			Key:    KeySchedulingFastLaneReserved,
			EnvVar: "SP_SCHEDULING_FAST_LANE_RESERVED",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				// Numeric parameters arrive as float64 from encoding/json.
				// 0 is a valid value (no reserved fast floor); negatives are
				// left to the worker's [0, pool_size−1] clamp.
				if v, ok := value.(float64); ok {
					cfg.Server.Scheduling.FastLaneReserved = int(v)
				} else if v, ok := value.(int); ok {
					cfg.Server.Scheduling.FastLaneReserved = v
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
				role, ok := value.(string)
				if !ok || role == "" {
					return
				}

				// This overlay runs after config.Validate(), so a role stored in
				// the database never went through startup validation. Applying an
				// unparseable one would silently switch off whichever subsystems
				// it fails to name — keep the validated value and say so loudly
				// instead (same best-effort contract as the password policy).
				if _, err := config.ParseNodeRoles(role); err != nil {
					slog.Warn(
						"Ignoring invalid node.role system parameter; keeping the configured role",
						"value", role, "configuredRole", cfg.Node.Role, "error", err,
					)

					return
				}

				cfg.Node.Role = role
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
			Key:    KeySessionMaxDuration,
			EnvVar: "SP_AUTH_SESSION_MAX_DURATION",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				// Seconds; 0 (or unset) means unlimited. Negative values are
				// treated as unset rather than clamped, since a negative cap
				// has no sensible meaning.
				if seconds, ok := parseInt(value); ok && seconds >= 0 {
					cfg.Auth.SessionMaxDuration = time.Duration(seconds) * time.Second
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
			// Tenant ID is part of the public Microsoft authorize/token URL, not a
			// credential -> Secret:false so it round-trips through GET /system/parameters
			// as plain text. Empty means the "common" multi-tenant default, which the
			// URL builders substitute (handlers/auth/microsoft.go, microsoft_service.go).
			// Trim surrounding whitespace so a stray space in a pasted GUID can't
			// silently corrupt the endpoint URL.
			Key:    KeyMicrosoftTenantID,
			EnvVar: "SP_MICROSOFT_TENANT_ID",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Microsoft.TenantID = strings.TrimSpace(v)
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
			Key:    KeySlackSocketModeEnabled,
			EnvVar: "SP_SLACK_SOCKET_MODE_ENABLED",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				cfg.Slack.SocketModeEnabled = parseBool(value, cfg.Slack.SocketModeEnabled)
			},
		},
		{
			Key:    KeySlackAppToken,
			EnvVar: "SP_SLACK_APP_TOKEN",
			Secret: true,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Slack.AppToken = v
				}
			},
		},
		{
			// Gates the whole Teams bot: routes stay registered but the
			// messaging endpoint refuses traffic while this is false. Default
			// false because Bot Framework requires a publicly reachable HTTPS
			// endpoint (no Socket-Mode equivalent exists).
			Key:    KeyMSTeamsEnabled,
			EnvVar: "SP_MSTEAMS_ENABLED",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				cfg.MSTeams.Enabled = parseBool(value, cfg.MSTeams.Enabled)
			},
		},
		{
			// The Entra application (client) ID. Public: it is the audience
			// every inbound Bot Framework token must carry and it is baked
			// into the generated Teams app manifest.
			Key:    KeyMSTeamsAppID,
			EnvVar: "SP_MSTEAMS_APP_ID",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.MSTeams.AppID = strings.TrimSpace(v)
				}
			},
		},
		{
			Key:    KeyMSTeamsAppSecret,
			EnvVar: "SP_MSTEAMS_APP_SECRET",
			Secret: true,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.MSTeams.AppSecret = v
				}
			},
		},
		{
			// Optional single-tenant allow-list. Empty = multi-tenant (SaaS),
			// accepting any installing tenant. Same public-identifier
			// reasoning as KeyMicrosoftTenantID above -> Secret:false.
			Key:    KeyMSTeamsTenantID,
			EnvVar: "SP_MSTEAMS_TENANT_ID",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.MSTeams.TenantID = strings.TrimSpace(v)
				}
			},
		},
		{
			// Kill switch only. Defaults to true and still yields a fully inert
			// integration until a project key is present — never flip this to
			// "analytics are on".
			Key:    KeyPostHogEnabled,
			EnvVar: EnvPostHogEnabled,
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				cfg.PostHog.Enabled = parseBool(value, cfg.PostHog.Enabled)
			},
		},
		{
			// The phc_… browser key. NOT a secret: it is shipped to every SPA
			// that loads the dashboard, exactly like a Sentry DSN.
			Key:    KeyPostHogProjectAPIKey,
			EnvVar: EnvPostHogProjectAPIKey,
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.PostHog.ProjectAPIKey = strings.TrimSpace(v)
				}
			},
		},
		{
			Key:    KeyPostHogHost,
			EnvVar: EnvPostHogHost,
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.PostHog.Host = strings.TrimSpace(v)
				}
			},
		},
		{
			// Server-side key. Secret: it carries broader privileges than the
			// project key and must never leave the process.
			Key:    KeyPostHogPersonalAPIKey,
			EnvVar: EnvPostHogPersonalAPIKey,
			Secret: true,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.PostHog.PersonalAPIKey = strings.TrimSpace(v)
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
			// The application's Ed25519 public key. NOT secret: it is a public
			// verification key Discord publishes on the app's own settings
			// page, and the dashboard needs to render it so an operator can
			// tell at a glance whether the value matches their Discord app.
			Key:    KeyDiscordPublicKey,
			EnvVar: "SP_DISCORD_PUBLIC_KEY",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.Discord.PublicKey = strings.TrimSpace(v)
				}
			},
		},
		{
			Key:    KeyDiscordGatewayEnabled,
			EnvVar: "SP_DISCORD_GATEWAY_ENABLED",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				cfg.Discord.GatewayEnabled = parseBool(value, cfg.Discord.GatewayEnabled)
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
			Key:    KeyOIDCEnabled,
			EnvVar: "SP_OIDC_ENABLED",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				cfg.OIDC.Enabled = parseBool(value, cfg.OIDC.Enabled)
			},
		},
		{
			Key:    KeyOIDCDisplayName,
			EnvVar: "SP_OIDC_DISPLAY_NAME",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.OIDC.DisplayName = v
				}
			},
		},
		{
			Key:    KeyOIDCIssuerURL,
			EnvVar: "SP_OIDC_ISSUER_URL",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.OIDC.IssuerURL = strings.TrimSpace(v)
				}
			},
		},
		{
			Key:    KeyOIDCClientID,
			EnvVar: "SP_OIDC_CLIENT_ID",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.OIDC.ClientID = v
				}
			},
		},
		{
			Key:    KeyOIDCClientSecret,
			EnvVar: "SP_OIDC_CLIENT_SECRET",
			Secret: true,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.OIDC.ClientSecret = v
				}
			},
		},
		{
			Key:    KeyOIDCScopes,
			EnvVar: "SP_OIDC_SCOPES",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.OIDC.Scopes = v
				}
			},
		},
		{
			Key:    KeyOIDCEmailClaim,
			EnvVar: "SP_OIDC_EMAIL_CLAIM",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.OIDC.EmailClaim = v
				}
			},
		},
		{
			Key:    KeyOIDCNameClaim,
			EnvVar: "SP_OIDC_NAME_CLAIM",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.OIDC.NameClaim = v
				}
			},
		},
		{
			Key:    KeyOIDCAvatarClaim,
			EnvVar: "SP_OIDC_AVATAR_CLAIM",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.OIDC.AvatarClaim = v
				}
			},
		},
		{
			Key:    KeySAMLEnabled,
			EnvVar: "SP_SAML_ENABLED",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				cfg.SAML.Enabled = parseBool(value, cfg.SAML.Enabled)
			},
		},
		{
			Key:    KeySAMLDisplayName,
			EnvVar: "SP_SAML_DISPLAY_NAME",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.SAML.DisplayName = v
				}
			},
		},
		{
			Key:    KeySAMLIDPMetadataURL,
			EnvVar: "SP_SAML_IDP_METADATA_URL",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.SAML.IDPMetadataURL = strings.TrimSpace(v)
				}
			},
		},
		{
			Key:    KeySAMLIDPMetadataXML,
			EnvVar: "SP_SAML_IDP_METADATA_XML",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.SAML.IDPMetadataXML = v
				}
			},
		},
		{
			Key:    KeySAMLSPEntityID,
			EnvVar: "SP_SAML_SP_ENTITY_ID",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.SAML.SPEntityID = strings.TrimSpace(v)
				}
			},
		},
		{
			Key:    KeySAMLEmailAttribute,
			EnvVar: "SP_SAML_EMAIL_ATTRIBUTE",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.SAML.EmailAttribute = v
				}
			},
		},
		{
			Key:    KeySAMLNameAttribute,
			EnvVar: "SP_SAML_NAME_ATTRIBUTE",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.SAML.NameAttribute = v
				}
			},
		},
		{
			Key:    KeyLDAPEnabled,
			EnvVar: "SP_LDAP_ENABLED",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				cfg.LDAP.Enabled = parseBool(value, cfg.LDAP.Enabled)
			},
		},
		{
			Key:    KeyLDAPServerURL,
			EnvVar: "SP_LDAP_SERVER_URL",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.LDAP.ServerURL = strings.TrimSpace(v)
				}
			},
		},
		{
			Key:    KeyLDAPStartTLS,
			EnvVar: "SP_LDAP_START_TLS",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				cfg.LDAP.StartTLS = parseBool(value, cfg.LDAP.StartTLS)
			},
		},
		{
			Key:    KeyLDAPInsecureSkipVerify,
			EnvVar: "SP_LDAP_INSECURE_SKIP_VERIFY",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				cfg.LDAP.InsecureSkipVerify = parseBool(value, cfg.LDAP.InsecureSkipVerify)
			},
		},
		{
			Key:    KeyLDAPBindDN,
			EnvVar: "SP_LDAP_BIND_DN",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.LDAP.BindDN = v
				}
			},
		},
		{
			Key:    KeyLDAPBindPassword,
			EnvVar: "SP_LDAP_BIND_PASSWORD",
			Secret: true,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.LDAP.BindPassword = v
				}
			},
		},
		{
			Key:    KeyLDAPBaseDN,
			EnvVar: "SP_LDAP_BASE_DN",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.LDAP.BaseDN = strings.TrimSpace(v)
				}
			},
		},
		{
			Key:    KeyLDAPUserFilter,
			EnvVar: "SP_LDAP_USER_FILTER",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.LDAP.UserFilter = v
				}
			},
		},
		{
			Key:    KeyLDAPEmailAttribute,
			EnvVar: "SP_LDAP_EMAIL_ATTRIBUTE",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.LDAP.EmailAttribute = v
				}
			},
		},
		{
			Key:    KeyLDAPNameAttribute,
			EnvVar: "SP_LDAP_NAME_ATTRIBUTE",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok {
					cfg.LDAP.NameAttribute = v
				}
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
		{
			Key:    KeyPasswordAlgorithm,
			EnvVar: "SP_AUTH_PASSWORD_ALGORITHM",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := value.(string); ok && v != "" {
					cfg.Auth.Password.Algorithm = v
				}
			},
		},
		{
			Key:    KeyPasswordArgon2Memory,
			EnvVar: "SP_AUTH_PASSWORD_ARGON2_MEMORY",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := parseUint32(value); ok {
					cfg.Auth.Password.Argon2.Memory = v
				}
			},
		},
		{
			Key:    KeyPasswordArgon2Time,
			EnvVar: "SP_AUTH_PASSWORD_ARGON2_TIME",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := parseUint32(value); ok {
					cfg.Auth.Password.Argon2.Time = v
				}
			},
		},
		{
			Key:    KeyPasswordArgon2Threads,
			EnvVar: "SP_AUTH_PASSWORD_ARGON2_THREADS",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := parseUint8(value); ok {
					cfg.Auth.Password.Argon2.Threads = v
				}
			},
		},
		{
			Key:    KeyPasswordArgon2KeyLen,
			EnvVar: "SP_AUTH_PASSWORD_ARGON2_KEY_LENGTH",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := parseUint32(value); ok {
					cfg.Auth.Password.Argon2.KeyLength = v
				}
			},
		},
		{
			Key:    KeyPasswordArgon2SaltLen,
			EnvVar: "SP_AUTH_PASSWORD_ARGON2_SALT_LENGTH",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := parseUint32(value); ok {
					cfg.Auth.Password.Argon2.SaltLength = v
				}
			},
		},
		{
			Key:    KeyPasswordBcryptCost,
			EnvVar: "SP_AUTH_PASSWORD_BCRYPT_COST",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				if v, ok := parseInt(value); ok {
					cfg.Auth.Password.Bcrypt.Cost = v
				}
			},
		},
		{
			Key:    KeyPasswordRehashOnLogin,
			EnvVar: "SP_AUTH_PASSWORD_REHASH_ON_LOGIN",
			Secret: false,
			ApplyFunc: func(cfg *config.Config, value any) {
				cfg.Auth.Password.RehashOnLogin = parseBool(value, cfg.Auth.Password.RehashOnLogin)
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

// parseUint32 coerces a config value to uint32 for the argon2 cost parameters.
// Accepts native int / float64 / numeric string. Negative or out-of-range values
// are rejected (ok=false) so the caller keeps its default. Write-time validation
// already enforces the real bounds; this only guards the coercion.
func parseUint32(value any) (uint32, bool) {
	n, ok := parseInt(value)
	if !ok || n < 0 || int64(n) > int64(^uint32(0)) {
		return 0, false
	}

	return uint32(n), true
}

// parseUint8 coerces a config value to uint8 for argon2 threads. Accepts native
// int / float64 / numeric string in [0,255]; rejects anything else.
func parseUint8(value any) (uint8, bool) {
	n, ok := parseInt(value)
	if !ok || n < 0 || n > 255 {
		return 0, false
	}

	return uint8(n), true
}

// Sentinel errors for aggregation-retention write validation. Wrapped (with the
// offending key/value) by ValidateAggregationRetentionParameter so callers can
// match on them while the message stays specific.
var (
	errRetentionNotInteger = errors.New("aggregation retention must be a whole number")
	errRetentionBelowFloor = errors.New("aggregation retention must be at least 1")
)

// IsAggregationRetentionKey reports whether key is one of the three live
// aggregation-retention parameters (raw hours / hourly days / daily months) the
// server "Aggregation" settings tab writes and the aggregation job reads at run
// time (see jobtypes.retentionFromConfig), so the write handler can route it
// through ValidateAggregationRetentionParameter.
func IsAggregationRetentionKey(key string) bool {
	pk := ParameterKey(key)

	return pk == KeyPerfAggRetentionRawHours ||
		pk == KeyPerfAggRetentionHourDays ||
		pk == KeyPerfAggRetentionDayMonths
}

// ValidateAggregationRetentionParameter enforces, at write time, the same floor
// the aggregation job requires: a whole number >= 1. A fractional, negative,
// below-floor, or non-numeric value is rejected so a PUT that the job would
// otherwise silently ignore (falling back to the default) never lands in the
// parameters table. Returns nil for keys that are not retention keys.
func ValidateAggregationRetentionParameter(key string, value any) error {
	if !IsAggregationRetentionKey(key) {
		return nil
	}

	n, ok := aggregationRetentionInt(value)
	if !ok {
		return fmt.Errorf("%s: %w", key, errRetentionNotInteger)
	}

	if n < 1 {
		return fmt.Errorf("%s (got %d): %w", key, n, errRetentionBelowFloor)
	}

	return nil
}

// aggregationRetentionInt coerces a write value to an integer, rejecting
// fractional floats (unlike parseInt, which truncates) so 1.5 is treated as
// invalid rather than silently floored to 1.
func aggregationRetentionInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		if typed != math.Trunc(typed) {
			return 0, false
		}

		return int(typed), true
	case float32:
		f := float64(typed)
		if f != math.Trunc(f) {
			return 0, false
		}

		return int(f), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, false
		}

		return n, true
	default:
		return 0, false
	}
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

	// Apply parameters with precedence: env > db > defaults.
	//
	// This is the AUTHORITATIVE overlay for every system parameter, including the
	// auth.password.* keys. For those keys it intentionally overlaps the early
	// env-only bootstrap in config.applyPasswordHashingEnv (see that function's
	// doc comment): config.Load applies env before the DB is up so an env-only
	// deployment still hashes correctly at boot, while this overlay is the only
	// place a DB-stored auth.password.* value can win. Env stays highest in both
	// paths, so the overlap is idempotent for env and additive for DB. After this
	// loop mutates cfg.Auth.Password.*, the caller (app.InitializeSystemConfig)
	// re-resolves and re-installs the hashing policy from the overlaid config.
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
