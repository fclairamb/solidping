package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/golang-jwt/jwt/v5"
)

// passkeySessionTTL is the lifetime of the sealed session JWT issued by
// Begin{Registration,Login} and consumed by Finish{Registration,Login}.
// Browsers cap WebAuthn ceremonies at ~30s on the user's side; 5min on
// our side leaves slack for slow networks and retries while staying
// well under the JWT key's threat window. Matches the 2FA temp token.
const passkeySessionTTL = 5 * time.Minute

// passkeySessionPurpose tags the JWT so a register session can't be
// replayed as a login session and vice versa.
type passkeySessionPurpose string

const (
	passkeySessionPurposeRegister passkeySessionPurpose = "passkey-register"
	passkeySessionPurposeLogin    passkeySessionPurpose = "passkey-login"
)

// passkeySessionClaims is the payload of the sealed JWT. We embed the
// raw webauthn.SessionData as JSON because the library expects it back
// untouched at Finish time — any field reshape breaks verification.
type passkeySessionClaims struct {
	Purpose    passkeySessionPurpose `json:"purpose"`
	UserUID    string                `json:"userUid,omitempty"` // populated for register; empty for discoverable login
	SessionRaw string                `json:"sd"`                // JSON-encoded webauthn.SessionData
	jwt.RegisteredClaims
}

func (s *Service) sealPasskeySession(
	purpose passkeySessionPurpose, userUID string, sd *webauthn.SessionData,
) (string, error) {
	raw, err := json.Marshal(sd)
	if err != nil {
		return "", fmt.Errorf("marshal session data: %w", err)
	}

	claims := passkeySessionClaims{
		Purpose:    purpose,
		UserUID:    userUID,
		SessionRaw: string(raw),
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(passkeySessionTTL)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	signed, err := token.SignedString([]byte(s.fullCfg.Auth.JWTSecret))
	if err != nil {
		return "", fmt.Errorf("sign passkey session: %w", err)
	}

	return signed, nil
}

// errPasskeySessionExpired and errPasskeySessionMismatch surface as
// distinct API error codes via the handler.
var (
	errPasskeySessionExpired = errors.New("passkey session expired")
	errPasskeySessionInvalid = errors.New("invalid passkey session")
)

func (s *Service) unsealPasskeySession(
	tokenString string, expectedPurpose passkeySessionPurpose,
) (*passkeySessionClaims, *webauthn.SessionData, error) {
	parsed, err := jwt.ParseWithClaims(tokenString, &passkeySessionClaims{},
		func(_ *jwt.Token) (any, error) {
			return []byte(s.fullCfg.Auth.JWTSecret), nil
		})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, nil, errPasskeySessionExpired
		}

		return nil, nil, errPasskeySessionInvalid
	}

	claims, ok := parsed.Claims.(*passkeySessionClaims)
	if !ok || !parsed.Valid {
		return nil, nil, errPasskeySessionInvalid
	}

	if claims.Purpose != expectedPurpose {
		return nil, nil, errPasskeySessionInvalid
	}

	var sd webauthn.SessionData
	if err := json.Unmarshal([]byte(claims.SessionRaw), &sd); err != nil {
		return nil, nil, fmt.Errorf("unmarshal session data: %w", err)
	}

	return claims, &sd, nil
}
