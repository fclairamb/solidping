package auth

import (
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// passkeyUser adapts a SolidPing user + their stored passkeys to the
// go-webauthn library's User interface. The WebAuthnID is the user's UID
// bytes — opaque to the authenticator, stable for the lifetime of the
// account, and not derivable from the email (so re-using the email
// doesn't collide with a deleted user's old passkeys).
type passkeyUser struct {
	user     *models.User
	passkeys []*models.UserPasskey
}

// WebAuthnID returns the user handle the authenticator stores.
func (u *passkeyUser) WebAuthnID() []byte {
	return []byte(u.user.UID)
}

// WebAuthnName is the user-visible identifier — displayed in account
// pickers. We use email since it's what users recognize.
func (u *passkeyUser) WebAuthnName() string {
	return u.user.Email
}

// WebAuthnDisplayName is the human-friendly name. Falls back to email
// when the profile name is empty.
func (u *passkeyUser) WebAuthnDisplayName() string {
	if u.user.Name != "" {
		return u.user.Name
	}

	return u.user.Email
}

// WebAuthnCredentials translates stored passkey rows into the library's
// Credential shape. The library uses these to build allowCredentials
// during BeginLogin and to verify the assertion during FinishLogin.
func (u *passkeyUser) WebAuthnCredentials() []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(u.passkeys))

	for _, passkey := range u.passkeys {
		transports := make([]protocol.AuthenticatorTransport, 0, len(passkey.Transports))
		for _, transport := range passkey.Transports {
			transports = append(transports, protocol.AuthenticatorTransport(transport))
		}

		cred := webauthn.Credential{
			ID:        passkey.CredentialID,
			PublicKey: passkey.PublicKey,
			Transport: transports,
			Flags: webauthn.CredentialFlags{
				UserPresent:    true,
				UserVerified:   passkey.UserVerified,
				BackupEligible: passkey.BackupEligible,
				BackupState:    passkey.BackupState,
			},
			Authenticator: webauthn.Authenticator{
				SignCount: passkey.SignCount,
			},
		}

		if passkey.AttestationFormat != nil {
			cred.AttestationFormat = *passkey.AttestationFormat
		}

		out = append(out, cred)
	}

	return out
}
