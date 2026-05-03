// Package signedurl produces and verifies short-lived HMAC signatures for
// public file URLs. The HMAC key is the JWT secret — rotating the secret
// invalidates every outstanding URL, which is the desired security property.
package signedurl

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// MaxSignedURLTTL caps how far in the future a signed URL may be valid.
// Anything longer is suspicious enough that we'd rather force a re-sign.
const MaxSignedURLTTL = 365 * 24 * time.Hour

// Errors returned by Verify.
var (
	ErrSignedURLExpired      = errors.New("signed URL expired")
	ErrSignedURLBadSignature = errors.New("signed URL has a bad signature")
)

// signatureLen is the number of hex chars kept from the HMAC output. 32 hex
// chars = 128 bits of MAC, plenty for a short-lived URL.
const signatureLen = 32

// Sign computes the (exp, sig) pair for a fileUID + ttl. ttl is clamped to
// MaxSignedURLTTL.
func Sign(secret []byte, fileUID uuid.UUID, ttl time.Duration) (int64, string) {
	if ttl > MaxSignedURLTTL {
		slog.Warn("signed URL TTL clamped",
			"requested", ttl.String(),
			"max", MaxSignedURLTTL.String(),
		)

		ttl = MaxSignedURLTTL
	}

	exp := time.Now().Add(ttl).Unix()
	sig := compute(secret, fileUID, exp)

	return exp, sig
}

// Verify checks the signature first (constant time), then the expiry. The
// order matters: we don't want a bad signature to leak whether the URL had
// expired (timing-side-channel hygiene).
func Verify(secret []byte, fileUID uuid.UUID, exp int64, sig string, now time.Time) error {
	expected := compute(secret, fileUID, exp)
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return ErrSignedURLBadSignature
	}

	if now.Unix() > exp {
		return ErrSignedURLExpired
	}

	return nil
}

// BuildURL composes the public URL: <baseURL>/pub/files/<fileUID>?exp=<exp>&sig=<sig>.
// baseURL must not include a trailing slash.
func BuildURL(baseURL string, fileUID uuid.UUID, exp int64, sig string) string {
	q := url.Values{}
	q.Set("exp", strconv.FormatInt(exp, 10))
	q.Set("sig", sig)

	return fmt.Sprintf("%s/pub/files/%s?%s", baseURL, fileUID.String(), q.Encode())
}

// compute returns the first signatureLen hex chars of HMAC-SHA256.
func compute(secret []byte, fileUID uuid.UUID, exp int64) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(fileUID.String()))
	mac.Write([]byte("."))
	mac.Write([]byte(strconv.FormatInt(exp, 10)))
	full := hex.EncodeToString(mac.Sum(nil))

	return full[:signatureLen]
}
