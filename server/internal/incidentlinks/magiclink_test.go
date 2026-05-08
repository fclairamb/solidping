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
