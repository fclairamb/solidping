package credentials

// Connection and provider settings carry secrets in the same shape as
// checker configs (a JSON map at rest), so we reuse SplitConfig/MergeConfig
// for them. The difference is that connections/providers don't go through
// the checker registry — there's no per-type Config interface to hang
// SecretFields() on. So we keep a small registry here, keyed by the
// type-string, listing the secret keys for each.

import "github.com/fclairamb/solidping/server/internal/db/models"

// Common secret-key constants.
const (
	providerKeyClientSecret = "client_secret"
	secretKeyAuthToken      = "auth_token"
)

// connectionSecretFields enumerates the secret JSON keys for each
// IntegrationConnection.Settings shape.
//
// URL fields are intentionally NOT secret: webhook `url` and the
// `webhook_url` of Discord / GoogleChat / Mattermost / MSTeams stay in the
// public `settings` JSONB so the dashboard can render them on the edit form.
// The threat model (DB-theft only, see server/CLAUDE.md) doesn't require
// encrypting endpoint URLs.
//
//nolint:gochecknoglobals // registry of secret-key declarations; treated as a constant lookup table
var connectionSecretFields = map[models.ConnectionType][]string{
	models.ConnectionTypeSlack:   {"access_token"},
	models.ConnectionTypeWebhook: {secretKeyAuthToken, "signingSecret", "signingSecretPrevious"},
	models.ConnectionTypeEmail:   {"smtp_password"},
	models.ConnectionTypeNtfy:    {secretKeyAuthToken},
	// Matrix: the bot/dedicated user's access token. homeserverUrl and roomId
	// stay public so the dashboard can render them on the edit form (same
	// reasoning as the webhook/Discord/GoogleChat/Mattermost/MSTeams URLs
	// above).
	models.ConnectionTypeMatrix:   {"accessToken"},
	models.ConnectionTypeOpsgenie: {"api_key"},
	models.ConnectionTypePushover: {"user_key", "api_token"},
	models.ConnectionTypeFreebox:  {"appToken"},
	// Kubernetes cluster credentials: a bearer token or a pasted kubeconfig.
	// The API server URL and CA cert stay public (endpoint URLs are not secret
	// under the DB-theft-only threat model — same as webhook URLs above).
	models.ConnectionTypeKubernetes: {"token", "kubeconfig"},
	// Twilio: only the account auth token is secret. The account SID, from /
	// messaging-service identifiers and recipient numbers stay public so the
	// dashboard can render them on the edit form.
	models.ConnectionTypeTwilio: {secretKeyAuthToken},
	// Microsoft Teams bot: credentials normally live in system config (one
	// Entra app per instance), so a connection's settings hold no secret in
	// the standard flow. `app_secret` is registered anyway so that any future
	// per-connection credential override is encrypted at rest by default
	// rather than silently landing in the public settings JSONB. Tenant id,
	// service URL and conversation references stay public — the dashboard
	// renders them on the setup page.
	models.ConnectionTypeMSTeamsBot: {"app_secret"},
}

// ConnectionSecretFields returns the secret keys for a connection type.
// Returns nil for unknown types — callers should treat that as "no secrets
// declared", which is the safe default (encryption opt-in per type).
func ConnectionSecretFields(connType models.ConnectionType) []string {
	fields, ok := connectionSecretFields[connType]
	if !ok {
		return nil
	}

	out := make([]string, len(fields))
	copy(out, fields)

	return out
}

// providerSecretFields enumerates the secret keys for each
// OrganizationProvider.Metadata shape. OAuth client secrets dominate this
// list — once stored, the dashboard never echoes them back.
//
//nolint:gochecknoglobals // registry of secret-key declarations; treated as a constant lookup table
var providerSecretFields = map[models.ProviderType][]string{
	models.ProviderTypeGoogle:    {providerKeyClientSecret},
	models.ProviderTypeGitHub:    {providerKeyClientSecret},
	models.ProviderTypeGitLab:    {providerKeyClientSecret},
	models.ProviderTypeMicrosoft: {providerKeyClientSecret},
	models.ProviderTypeOIDC:      {providerKeyClientSecret},
}

// ProviderSecretFields returns the secret keys for a provider type. Same
// nil-on-unknown contract as ConnectionSecretFields.
func ProviderSecretFields(providerType models.ProviderType) []string {
	fields, ok := providerSecretFields[providerType]
	if !ok {
		return nil
	}

	out := make([]string, len(fields))
	copy(out, fields)

	return out
}
