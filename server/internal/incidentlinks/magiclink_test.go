package incidentlinks_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/incidentlinks"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	secret := []byte("top-secret-key")
	incidentUID := "inc-123"
	email := "alice@example.com"
	exp := time.Now().Add(time.Hour)

	token := incidentlinks.Sign(secret, incidentUID, email, exp)
	r.NotEmpty(token)
	r.Contains(token, ".") // payload.sig

	payload, err := incidentlinks.Verify(secret, incidentUID, token)
	r.NoError(err)
	r.Equal(incidentUID, payload.IncidentUID)
	r.Equal(email, payload.RecipientEmail)
	r.WithinDuration(exp, payload.Expiry, time.Second)
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	token := incidentlinks.Sign([]byte("secret-a"), "inc", "u@x", time.Now().Add(time.Hour))

	_, err := incidentlinks.Verify([]byte("secret-b"), "inc", token)
	r.ErrorIs(err, incidentlinks.ErrSignature)
}

func TestVerifyRejectsExpired(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	secret := []byte("k")
	token := incidentlinks.Sign(secret, "inc", "u@x", time.Now().Add(-time.Minute))

	_, err := incidentlinks.Verify(secret, "inc", token)
	r.ErrorIs(err, incidentlinks.ErrExpired)
}

func TestVerifyRejectsIncidentMismatch(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	secret := []byte("k")
	token := incidentlinks.Sign(secret, "inc-A", "u@x", time.Now().Add(time.Hour))

	_, err := incidentlinks.Verify(secret, "inc-B", token)
	r.ErrorIs(err, incidentlinks.ErrIncidentMismatch)
}

func TestVerifyRejectsMalformed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	secret := []byte("k")

	for _, bad := range []string{"", "no-dot", "one.two.three", strings.Repeat("!", 8) + "." + strings.Repeat("!", 8)} {
		_, err := incidentlinks.Verify(secret, "inc", bad)
		r.ErrorIs(err, incidentlinks.ErrMalformed, "input=%q", bad)
	}
}

// --- Unsubscribe token tests (D4) ---

func TestSignVerifyUnsubscribeRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	secret := []byte("top-secret-key")
	orgSlug := "acme"
	email := "alice@example.com"
	checkUID := "check-123"
	exp := time.Now().Add(90 * 24 * time.Hour)

	token := incidentlinks.SignUnsubscribe(secret, orgSlug, email, checkUID, exp)
	r.NotEmpty(token)
	r.Contains(token, ".")

	payload, err := incidentlinks.VerifyUnsubscribe(secret, token)
	r.NoError(err)
	r.Equal(orgSlug, payload.OrgSlug)
	r.Equal(email, payload.Email)
	r.Equal(checkUID, payload.CheckUID)
	r.WithinDuration(exp, payload.Expiry, time.Second)
}

func TestSignVerifyUnsubscribeRoundTrip_OrgWide(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	secret := []byte("k")

	// Empty checkUID means "all checks in the org" — the org-wide scope.
	token := incidentlinks.SignUnsubscribe(secret, "acme", "alice@example.com", "", time.Now().Add(time.Hour))

	payload, err := incidentlinks.VerifyUnsubscribe(secret, token)
	r.NoError(err)
	r.Empty(payload.CheckUID, "empty check UID means org-wide scope")
}

func TestVerifyUnsubscribeRejectsWrongSecret(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	token := incidentlinks.SignUnsubscribe([]byte("secret-a"), "acme", "u@x", "", time.Now().Add(time.Hour))

	_, err := incidentlinks.VerifyUnsubscribe([]byte("secret-b"), token)
	r.ErrorIs(err, incidentlinks.ErrSignature)
}

func TestVerifyUnsubscribeRejectsExpired(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	secret := []byte("k")
	token := incidentlinks.SignUnsubscribe(secret, "acme", "u@x", "", time.Now().Add(-time.Minute))

	_, err := incidentlinks.VerifyUnsubscribe(secret, token)
	r.ErrorIs(err, incidentlinks.ErrExpired)
}

func TestVerifyUnsubscribeRejectsMalformed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	secret := []byte("k")

	for _, bad := range []string{"", "no-dot", "one.two.three", strings.Repeat("!", 8) + "." + strings.Repeat("!", 8)} {
		_, err := incidentlinks.VerifyUnsubscribe(secret, bad)
		r.ErrorIs(err, incidentlinks.ErrMalformed, "input=%q", bad)
	}
}

// --- Purpose separation (spec acceptance criterion 5) ---
//
// A real security property: an ack token must never verify as an
// unsubscribe token, and an unsubscribe token must never verify as an ack
// token — even though both are HMAC-signed with the same secret and share
// the same wire framing (base64(payload).base64(sig)). Both directions are
// tested explicitly below.

func TestPurposeSeparation_AckTokenRejectedByVerifyUnsubscribe(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	secret := []byte("shared-secret")

	// A validly-signed ack token …
	ackToken := incidentlinks.Sign(secret, "inc-123", "alice@example.com", time.Now().Add(time.Hour))

	// … must be rejected by the unsubscribe verifier, with the specific
	// ErrPurposeMismatch sentinel (not just "some error") so the handler can
	// map it to the right response.
	_, err := incidentlinks.VerifyUnsubscribe(secret, ackToken)
	r.ErrorIs(err, incidentlinks.ErrPurposeMismatch,
		"an ack token must never be usable as an unsubscribe token")
}

func TestPurposeSeparation_UnsubscribeTokenRejectedByVerify(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	secret := []byte("shared-secret")

	// A validly-signed unsubscribe token …
	unsubToken := incidentlinks.SignUnsubscribe(secret, "acme", "alice@example.com", "check-1", time.Now().Add(time.Hour))

	// … must be rejected by the ack verifier, with ErrPurposeMismatch.
	_, err := incidentlinks.Verify(secret, "inc-123", unsubToken)
	r.ErrorIs(err, incidentlinks.ErrPurposeMismatch,
		"an unsubscribe token must never be usable as an ack token")
}

// TestPurposeSeparation_BothTokensStillVerifyForTheirOwnPurpose is a sanity
// companion to the two rejection tests above — confirms the mismatch checks
// don't accidentally reject legitimate same-purpose verification too (i.e.
// the fix isn't "reject everything").
func TestPurposeSeparation_BothTokensStillVerifyForTheirOwnPurpose(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	secret := []byte("shared-secret")
	exp := time.Now().Add(time.Hour)

	ackToken := incidentlinks.Sign(secret, "inc-123", "alice@example.com", exp)
	_, err := incidentlinks.Verify(secret, "inc-123", ackToken)
	r.NoError(err)

	unsubToken := incidentlinks.SignUnsubscribe(secret, "acme", "alice@example.com", "check-1", exp)
	_, err = incidentlinks.VerifyUnsubscribe(secret, unsubToken)
	r.NoError(err)
}
