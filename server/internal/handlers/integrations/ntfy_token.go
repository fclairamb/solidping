package integrations

import (
	"maps"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ntfy access-token keys: auth_token is the canonical one, the key the
// dashboard writes. accessToken is the legacy name, still accepted from API,
// Terraform or Ansible clients and still read by the sender as a fallback.
const (
	ntfySettingAuthToken       = "auth_token"
	ntfyLegacySettingAuthToken = "accessToken"
)

// canonicalizeNtfyToken renames a supplied legacy ntfy `accessToken` to
// `auth_token` when the settings carry no `auth_token`, so new and updated
// rows only ever store the canonical key. Settings of other connection types,
// and settings that already carry `auth_token`, are returned unchanged. The
// input map is never mutated.
func canonicalizeNtfyToken(connType models.ConnectionType, settings map[string]any) map[string]any {
	if connType != models.ConnectionTypeNtfy {
		return settings
	}

	legacy, hasLegacy := settings[ntfyLegacySettingAuthToken]
	if !hasLegacy {
		return settings
	}

	if current, ok := settings[ntfySettingAuthToken].(string); ok && current != "" {
		return settings
	}

	out := maps.Clone(settings)
	out[ntfySettingAuthToken] = legacy
	delete(out, ntfyLegacySettingAuthToken)

	return out
}
