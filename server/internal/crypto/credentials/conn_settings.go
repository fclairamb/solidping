package credentials

// Reading and writing an integration's secret settings is one operation with
// one set of rules, but it used to be copy-pasted at every call site — and
// every copy that was missed became a bot token that "disappeared" the moment
// the split happened (spec 2026-09-18-02). The two functions here are that
// operation, so a new reader can only be correct.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ErrEncryptionDisabled is returned when a connection's private settings are
// sealed with the instance master key but this process has none, so the
// secrets cannot be opened at all. Deliberately distinct from "the connection
// has no secret": one is an operator configuration problem, the other is a
// connection that was never finished.
var ErrEncryptionDisabled = errors.New(
	"credentials encryption disabled — see SP_ENCRYPTION_MASTER_KEY",
)

// OpenConnectionSettings returns the effective (public ∪ private) settings of
// an integration, decrypting the private envelope when there is one.
//
// The input row is never mutated: callers may hand in a cached or shared
// *models.Integration, and a merged token must not leak back into the public
// Settings map they hold.
//
// Rules, in order:
//   - no private envelope → a copy of Settings (pre-split rows keep working,
//     no migration required);
//   - a plaintext envelope (the documented no-master-key self-hosted
//     fallback) → opened with no key at all, so a keyless deployment still
//     reads its own secrets;
//   - a sealed envelope → needs a usable credentials service, else
//     ErrEncryptionDisabled.
func OpenConnectionSettings(
	ctx context.Context, creds Service, conn *models.Integration,
) (map[string]any, error) {
	if conn == nil {
		return map[string]any{}, nil
	}

	public := make(map[string]any, len(conn.Settings))
	for k, v := range conn.Settings {
		public[k] = v
	}

	if conn.SettingsPrivate == nil || *conn.SettingsPrivate == "" {
		return public, nil
	}

	envelope := *conn.SettingsPrivate

	if !RequiresKey(envelope) {
		private, err := OpenPlaintext(envelope)
		if err != nil {
			return nil, fmt.Errorf("open plaintext connection settings %s: %w", conn.UID, err)
		}

		return MergeConfig(public, private), nil
	}

	// A nil service can open nothing, so it always fails here.
	if creds == nil || !creds.Enabled() {
		return nil, fmt.Errorf("decrypt connection %s: %w", conn.UID, ErrEncryptionDisabled)
	}

	private, err := creds.DecryptForOrg(ctx, conn.OrganizationUID, envelope)
	if err != nil {
		return nil, fmt.Errorf("decrypt connection %s: %w", conn.UID, err)
	}

	return MergeConfig(public, private), nil
}

// SealedSettings is the storage shape of a connection's settings after the
// secret keys have been split out of the public column.
type SealedSettings struct {
	// Public is what goes in the `settings` JSONB — never a secret value.
	Public map[string]any
	// Private is the envelope for the `settings_private` column, nil when the
	// connection type declares no secret or none was supplied.
	Private *string
	// PrivateKeys is the JSON array of sealed key names for
	// `settings_private_keys`, so the dashboard can render placeholder pills.
	PrivateKeys *string
}

// SealConnectionSettings splits effective settings into public/private using
// the connection type's declared secret keys and seals the private half.
//
// Secrets are ALWAYS split out of the public `settings` column, in every mode
// (spec 2026-07-18-06): an AES-GCM envelope when a master key is configured, a
// clearly-marked plaintext envelope otherwise. Both the integrations write
// path and the Slack OAuth install path go through this, so a freshly
// installed app is stored exactly like an edited one.
func SealConnectionSettings(
	ctx context.Context,
	creds Service,
	connType models.ConnectionType,
	orgUID string,
	effective map[string]any,
) (*SealedSettings, error) {
	if effective == nil {
		effective = map[string]any{}
	}

	public, private := SplitConfig(effective, ConnectionSecretFields(connType))

	out := &SealedSettings{Public: public}
	if len(private) == 0 {
		return out, nil
	}

	var (
		envelope string
		err      error
	)

	if creds != nil && creds.Enabled() {
		envelope, err = creds.EncryptForOrg(ctx, orgUID, private)
		if err != nil {
			return nil, fmt.Errorf("encrypt connection settings: %w", err)
		}
	} else {
		envelope, err = SealPlaintext(private)
		if err != nil {
			return nil, fmt.Errorf("seal plaintext connection settings: %w", err)
		}
	}

	keysJSON, err := json.Marshal(SortedKeys(private))
	if err != nil {
		return nil, fmt.Errorf("marshal settings private keys: %w", err)
	}

	keysStr := string(keysJSON)
	out.Private = &envelope
	out.PrivateKeys = &keysStr

	return out, nil
}
