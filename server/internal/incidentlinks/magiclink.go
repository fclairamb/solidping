// Package incidentlinks signs and verifies the magic-link tokens used by
// incident notification emails. Lives in its own leaf package because both
// the email sender (notifications) and the HTTP handlers (handlers/incidents,
// handlers/unsubscribe) need it — putting it in either would create an import
// cycle.
//
// Tokens are HMAC-SHA256 over a compact, purpose-prefixed payload signed with
// the existing JWT secret. The leading `<purpose>|` segment domain-separates
// token kinds: an "ack" token can never verify as an "unsub" token and vice
// versa, because Verify/VerifyUnsubscribe each check the purpose segment
// before trusting anything else in the payload (spec 2026-07-05-10,
// acceptance criterion 5). Verifying an ack token yields both the incident
// UID and the recipient email; verifying an unsubscribe token yields the org
// slug, recipient email, and check scope — so the audit trail is complete
// even when the recipient never logged into the dashboard.
//
// Threat-model caveat (per the spec): rotating SP_AUTH_JWT_SECRET invalidates
// every outstanding token. Per-token revocation lists are explicitly out of
// scope for v1.
package incidentlinks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Token purpose prefixes. Every signed payload begins with one of these
// followed by "|" — this is what makes an ack token and an unsubscribe token
// mutually unusable as each other, even though both are HMAC-signed with the
// same secret.
const (
	// PurposeAck prefixes magic-link acknowledge tokens.
	PurposeAck = "ack"
	// PurposeUnsubscribe prefixes unsubscribe tokens.
	PurposeUnsubscribe = "unsub"
)

// Magic-link token errors. Kept as sentinels so the handler can map each
// failure mode to a different HTML page (expired vs tampered vs malformed).
var (
	// ErrMalformed means the token doesn't decode into the expected
	// pipe-delimited shape for its purpose.
	ErrMalformed = errors.New("magic-link token: malformed")
	// ErrSignature means the HMAC didn't match — either tampered or
	// signed with a different secret.
	ErrSignature = errors.New("magic-link token: invalid signature")
	// ErrExpired means the embedded expiry is in the past.
	ErrExpired = errors.New("magic-link token: expired")
	// ErrIncidentMismatch means the URL incident UID and the token incident
	// UID don't agree — defends against pasting a token from one incident
	// into another's URL.
	ErrIncidentMismatch = errors.New("magic-link token: incident mismatch")
	// ErrPurposeMismatch means the token's leading purpose segment does not
	// match what the verifying endpoint expects — e.g. an ack token
	// presented to the unsubscribe endpoint, or vice versa. This is the
	// domain-separation guarantee: signature validity alone is not enough,
	// the purpose must match too.
	ErrPurposeMismatch = errors.New("magic-link token: purpose mismatch")
)

// AckTokenPayload is the verified payload returned to the handler for an ack
// token.
type AckTokenPayload struct {
	IncidentUID    string
	RecipientEmail string
	Expiry         time.Time
}

// UnsubscribeTokenPayload is the verified payload returned to the handler for
// an unsubscribe token.
type UnsubscribeTokenPayload struct {
	OrgSlug string
	Email   string
	// CheckUID is "" for an org-wide unsubscribe (every check in the org).
	CheckUID string
	Expiry   time.Time
}

// sign builds a token: base64(purpose|field1|field2|...|unix-exp).sig,
// where sig is the URL-safe base64 HMAC-SHA256 of the payload bytes. Shared
// by Sign and SignUnsubscribe so both purposes use byte-identical framing.
func sign(secret []byte, payload string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	sig := mac.Sum(nil)

	return base64.RawURLEncoding.EncodeToString([]byte(payload)) +
		"." +
		base64.RawURLEncoding.EncodeToString(sig)
}

// verify decodes and HMAC-verifies a token, then checks its leading purpose
// segment against wantPurpose BEFORE validating anything else about the
// payload shape. This ordering is what makes purpose mismatches
// distinguishable from malformed tokens: an ack token (4 fields:
// ack|incident|email|exp) and an unsubscribe token (5 fields:
// unsub|org|email|check|exp) have DIFFERENT field counts by design, so if
// field-count validation ran first, presenting one to the other's verifier
// would report ErrMalformed — masking the more specific, more important
// ErrPurposeMismatch a caller needs to detect cross-purpose token replay.
// Checking the purpose first means the field-count check only ever runs on
// a token that already claims to be the right purpose.
//
// Returns the pipe-split fields (purpose included as fields[0]) with the
// trailing exp field already validated for non-expiry. Shared by Verify and
// VerifyUnsubscribe.
func verify(secret []byte, token, wantPurpose string, wantFields int) ([]string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, ErrMalformed
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrMalformed
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrMalformed
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(payloadBytes)
	expected := mac.Sum(nil)

	if !hmac.Equal(expected, sigBytes) {
		return nil, ErrSignature
	}

	fields := strings.Split(string(payloadBytes), "|")

	// Purpose check comes before the field-count check (see doc comment):
	// a correctly-signed token of the OTHER purpose has a different field
	// count by construction, and that difference must not masquerade as
	// "malformed" — it is a purpose mismatch, a distinct and more actionable
	// failure mode.
	if len(fields) == 0 || fields[0] != wantPurpose {
		return nil, ErrPurposeMismatch
	}

	if len(fields) != wantFields {
		return nil, ErrMalformed
	}

	expUnix, err := strconv.ParseInt(fields[len(fields)-1], 10, 64)
	if err != nil {
		return nil, ErrMalformed
	}

	if time.Now().After(time.Unix(expUnix, 0)) {
		return nil, ErrExpired
	}

	return fields, nil
}

// Sign builds a magic-link ack token. exp is the absolute expiry time (the
// spec uses incident.created_at + 7d). secret is the bytes of the HMAC key —
// the caller passes []byte(cfg.Auth.JWTSecret).
//
// Token shape (URL-safe base64, no padding): `<b64-payload>.<b64-sig>` where
// payload = `ack|<incident>|<email>|<unix-exp>`. The leading "ack|" purpose
// tag is what Verify checks before trusting the rest of the payload — an
// unsubscribe token built by SignUnsubscribe can never pass Verify, because
// its payload starts with "unsub|" instead.
func Sign(secret []byte, incidentUID, recipientEmail string, exp time.Time) string {
	payload := PurposeAck + "|" + incidentUID + "|" + recipientEmail + "|" + strconv.FormatInt(exp.Unix(), 10)

	return sign(secret, payload)
}

// Verify parses and validates an ack token issued by Sign. expectedIncidentUID
// is the URL's incident UID — verifying it inside the payload defends against
// re-using a token across incidents. Returns ErrPurposeMismatch (not
// ErrMalformed) for a token whose purpose segment isn't "ack" — including a
// well-formed, correctly-signed unsubscribe token — so the failure mode is
// distinguishable from a corrupted token.
func Verify(secret []byte, expectedIncidentUID, token string) (*AckTokenPayload, error) {
	fields, err := verify(secret, token, PurposeAck, 4) // ack|incident|email|exp
	if err != nil {
		return nil, err
	}

	if fields[1] != expectedIncidentUID {
		return nil, ErrIncidentMismatch
	}

	expUnix, _ := strconv.ParseInt(fields[3], 10, 64) // already validated by verify()

	return &AckTokenPayload{
		IncidentUID:    fields[1],
		RecipientEmail: fields[2],
		Expiry:         time.Unix(expUnix, 0),
	}, nil
}

// SignUnsubscribe builds an unsubscribe token. checkUID is "" for an
// org-wide unsubscribe (every check in the org); non-empty scopes the
// suppression to one check. exp is the absolute expiry (spec: 90 days from
// issue — longer than ack's 7 days because these get clicked long after
// delivery).
//
// Token shape mirrors Sign: payload = `unsub|<org-slug>|<email>|<check-uid-or-empty>|<unix-exp>`.
// The "unsub|" purpose tag means this token can never pass Verify (the ack
// verifier), and an ack token can never pass VerifyUnsubscribe — both check
// the leading segment before trusting anything else.
func SignUnsubscribe(secret []byte, orgSlug, email, checkUID string, exp time.Time) string {
	payload := PurposeUnsubscribe + "|" + orgSlug + "|" + email + "|" + checkUID + "|" + strconv.FormatInt(exp.Unix(), 10)

	return sign(secret, payload)
}

// VerifyUnsubscribe parses and validates an unsubscribe token issued by
// SignUnsubscribe. Returns ErrPurposeMismatch for a token whose purpose
// segment isn't "unsub" — including a well-formed, correctly-signed ack
// token — so an ack link can never be replayed against the unsubscribe
// endpoint (spec acceptance criterion 5, the reverse direction of Verify's
// guarantee above).
func VerifyUnsubscribe(secret []byte, token string) (*UnsubscribeTokenPayload, error) {
	fields, err := verify(secret, token, PurposeUnsubscribe, 5) // unsub|org|email|check|exp
	if err != nil {
		return nil, err
	}

	expUnix, _ := strconv.ParseInt(fields[4], 10, 64) // already validated by verify()

	return &UnsubscribeTokenPayload{
		OrgSlug:  fields[1],
		Email:    fields[2],
		CheckUID: fields[3],
		Expiry:   time.Unix(expUnix, 0),
	}, nil
}
