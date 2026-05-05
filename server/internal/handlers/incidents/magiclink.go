package incidents

// Magic-link ack tokens. HMAC-SHA256 over a compact `<incident_uid>|<email>|<exp>`
// payload signed with the existing JWT secret. The token authenticates *and*
// identifies the recipient — verifying it yields both the incident UID and
// the email that received the notification, so the audit trail is complete
// even when the recipient never logged into the dashboard.
//
// Threat-model caveat (per the spec): rotating SP_AUTH_JWT_SECRET invalidates
// every outstanding token. Per-token revocation lists are explicitly out of
// scope for v1.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Magic-link token errors. Kept as sentinels so the handler can map each
// failure mode to a different HTML page (expired vs tampered vs malformed).
var (
	// ErrAckTokenMalformed means the token doesn't decode into the expected
	// `<incident>|<email>|<exp>|<sig>` shape.
	ErrAckTokenMalformed = errors.New("ack token: malformed")
	// ErrAckTokenSignature means the HMAC didn't match — either tampered or
	// signed with a different secret.
	ErrAckTokenSignature = errors.New("ack token: invalid signature")
	// ErrAckTokenExpired means the embedded expiry is in the past.
	ErrAckTokenExpired = errors.New("ack token: expired")
	// ErrAckTokenIncidentMismatch means the URL incident UID and the token
	// incident UID don't agree — defends against pasting a token from one
	// incident into another's URL.
	ErrAckTokenIncidentMismatch = errors.New("ack token: incident mismatch")
)

// AckTokenPayload is the verified payload returned to the handler.
type AckTokenPayload struct {
	IncidentUID    string
	RecipientEmail string
	Expiry         time.Time
}

// SignAckToken builds a magic-link ack token. exp is the absolute expiry
// time (the spec uses incident.created_at + 7d). secret is the bytes of the
// HMAC key — the caller passes []byte(cfg.Auth.JWTSecret).
//
// Token shape (URL-safe base64, no padding): `<b64-payload>.<b64-sig>` where
// payload = `<incident>|<email>|<unix-exp>`.
func SignAckToken(secret []byte, incidentUID, recipientEmail string, exp time.Time) string {
	payload := incidentUID + "|" + recipientEmail + "|" + strconv.FormatInt(exp.Unix(), 10)

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	sig := mac.Sum(nil)

	return base64.RawURLEncoding.EncodeToString([]byte(payload)) +
		"." +
		base64.RawURLEncoding.EncodeToString(sig)
}

// VerifyAckToken parses and validates a token issued by SignAckToken.
// expectedIncidentUID is the URL's incident UID — verifying it inside the
// payload defends against re-using a token across incidents.
func VerifyAckToken(secret []byte, expectedIncidentUID, token string) (*AckTokenPayload, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, ErrAckTokenMalformed
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrAckTokenMalformed
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrAckTokenMalformed
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(payloadBytes)
	expected := mac.Sum(nil)

	if !hmac.Equal(expected, sigBytes) {
		return nil, ErrAckTokenSignature
	}

	fields := strings.Split(string(payloadBytes), "|")
	if len(fields) != 3 {
		return nil, ErrAckTokenMalformed
	}

	expUnix, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return nil, ErrAckTokenMalformed
	}

	exp := time.Unix(expUnix, 0)
	if time.Now().After(exp) {
		return nil, ErrAckTokenExpired
	}

	if fields[0] != expectedIncidentUID {
		return nil, ErrAckTokenIncidentMismatch
	}

	return &AckTokenPayload{
		IncidentUID:    fields[0],
		RecipientEmail: fields[1],
		Expiry:         exp,
	}, nil
}
