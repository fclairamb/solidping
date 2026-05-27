package credentials

// Connection and provider settings carry secrets in the same shape as
// checker configs (a JSON map at rest), so we reuse SplitConfig/MergeConfig
// for them. The difference is that connections/providers don't go through
// the checker registry — there's no per-type Config interface to hang
// SecretFields() on. So we keep a small registry here, keyed by the
// type-string, listing the secret keys for each.

import "github.com/fclairamb/solidping/server/internal/db/models"

// Common secret-key constants. Multiple connection / provider types use
// the same key name (e.g., "webhook_url" for Discord/Mattermost/GoogleChat),
// so we factor them here to satisfy the goconst linter and keep the
// registry tables tidy.
const (
	connKeyWebhookURL       = "webhook_url"
	providerKeyClientSecret = "client_secret"
)

// connectionSecretFields enumerates the secret JSON keys for each
// IntegrationConnection.Settings shape.
//
//nolint:gochecknoglobals // registry of secret-key declarations; treated as a constant lookup table
var connectionSecretFields = map[models.ConnectionType][]string{
	models.ConnectionTypeSlack:      {"access_token"},
	models.ConnectionTypeDiscord:    {connKeyWebhookURL},
	models.ConnectionTypeWebhook:    {"url", "auth_token", "signingSecret", "signingSecretPrevious"},
	models.ConnectionTypeEmail:      {"smtp_password"},
	models.ConnectionTypeGoogleChat: {connKeyWebhookURL},
	models.ConnectionTypeMattermost: {connKeyWebhookURL},
	models.ConnectionTypeNtfy:       {"auth_token"},
	models.ConnectionTypeOpsgenie:   {"api_key"},
	models.ConnectionTypePushover:   {"user_key", "api_token"},
	models.ConnectionTypeFreebox:    {"appToken"},
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
