package signedurl_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/files/signedurl"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	secret := []byte("secret-key")
	fileUID := uuid.New()

	exp, sig := signedurl.Sign(secret, fileUID, time.Hour)
	r.NotEmpty(sig)
	r.Greater(exp, time.Now().Unix())

	r.NoError(signedurl.Verify(secret, fileUID, exp, sig, time.Now()))
}

func TestVerify_BadSignature(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	secret := []byte("secret-key")
	fileUID := uuid.New()

	exp, sig := signedurl.Sign(secret, fileUID, time.Hour)

	// Tamper deterministically: flip the first hex char to a different
	// value (sig[0] may itself be "0", so prefixing with "0" is a no-op).
	flip := byte('1')
	if sig[0] == '1' {
		flip = '2'
	}
	tampered := string(flip) + sig[1:]
	r.ErrorIs(
		signedurl.Verify(secret, fileUID, exp, tampered, time.Now()),
		signedurl.ErrSignedURLBadSignature,
	)

	// Different fileUID also fails
	r.ErrorIs(
		signedurl.Verify(secret, uuid.New(), exp, sig, time.Now()),
		signedurl.ErrSignedURLBadSignature,
	)
}

func TestVerify_Expired(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	secret := []byte("secret-key")
	fileUID := uuid.New()

	exp, sig := signedurl.Sign(secret, fileUID, time.Hour)

	// Clock skewed past expiry
	r.ErrorIs(
		signedurl.Verify(secret, fileUID, exp, sig, time.Unix(exp+1, 0)),
		signedurl.ErrSignedURLExpired,
	)
}

func TestSign_TTLClamp(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	secret := []byte("secret-key")
	fileUID := uuid.New()

	// Request 10 years; expect clamping to MaxSignedURLTTL
	exp, sig := signedurl.Sign(secret, fileUID, 10*365*24*time.Hour)
	r.NotEmpty(sig)

	// exp should be approximately now + MaxSignedURLTTL
	maxExp := time.Now().Add(signedurl.MaxSignedURLTTL).Unix()
	r.LessOrEqual(exp, maxExp+1)
	r.GreaterOrEqual(exp, maxExp-1)
}

func TestBuildURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fileUID := uuid.New()
	url := signedurl.BuildURL("https://example.com", fileUID, 12345, "abcd")
	r.True(strings.HasPrefix(url, "https://example.com/pub/files/"+fileUID.String()+"?"))
	r.Contains(url, "exp=12345")
	r.Contains(url, "sig=abcd")
}
