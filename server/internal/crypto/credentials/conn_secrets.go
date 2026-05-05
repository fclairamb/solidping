package credentials

// Connection and provider settings carry secrets in the same shape as
// checker configs (a JSON map at rest), so we reuse SplitConfig/MergeConfig
// for them. The difference is that connections/providers don't go through
// the checker registry — there's no per-type Config interface to hang
// SecretFields() on. So we keep a small registry here, keyed by the
// type-string, listing the secret keys for each.

import "github.com/fclairamb/solidping/server/internal/db/models"

// connectionSecretFields enumerates the secret JSON keys for each
// IntegrationConnection.Settings shape.
var connectionSecretFields = map[models.ConnectionType][]string{
	models.ConnectionTypeSlack:      {"access_token"},
	models.ConnectionTypeDiscord:    {"webhook_url"},
	models.ConnectionTypeWebhook:    {"url", "auth_token"},
	models.ConnectionTypeEmail:      {"smtp_password"},
	models.ConnectionTypeGoogleChat: {"webhook_url"},
	models.ConnectionTypeMattermost: {"webhook_url"},
	models.ConnectionTypeNtfy:       {"auth_token"},
	models.ConnectionTypeOpsgenie:   {"api_key"},
	models.ConnectionTypePushover:   {"user_key", "api_token"},
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
var providerSecretFields = map[models.ProviderType][]string{
	models.ProviderTypeGoogle:    {"client_secret"},
	models.ProviderTypeGitHub:    {"client_secret"},
	models.ProviderTypeGitLab:    {"client_secret"},
	models.ProviderTypeMicrosoft: {"client_secret"},
	models.ProviderTypeOIDC:      {"client_secret"},
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
