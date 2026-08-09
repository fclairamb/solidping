// Package servicesig implements the HMAC request-signing scheme used between
// SolidPing and the separate billing service (solidping-billing), in both
// directions.
//
// A request is signed over the canonical string
//
//	<timestamp>.<METHOD>.<path>.<hex sha256 of raw body>
//
// with HMAC-SHA256, carried in three headers:
//
//	X-SP-Signature: v1,<base64 HMAC>
//	X-SP-Timestamp: <unix seconds, part of the signed string>
//	X-SP-Key-Id:    <which shared key signed it>
//
// This is a wire contract shared with solidping-billing: the canonical string
// and the header names are pinned by tests on both sides and must not be
// changed without changing both repos. It deliberately differs from Standard
// Webhooks (which signs `id.timestamp.body`) — do not try to share code with
// the Polar webhook path.
//
// Keys are held as an ordered set of {id, secret} pairs, newest first: signers
// use the first entry, verifiers accept any entry. That is what makes rotation
// a two-step config change (add the new key on both sides, then drop the old)
// rather than a coordinated restart.
//
// There is one independent key set per direction, so a leak of the key that
// signs outbound calls cannot be used to forge inbound entitlement writes.
package servicesig

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Signature headers. Same names in both directions and in both repos.
const (
	HeaderSignature = "X-SP-Signature"
	HeaderTimestamp = "X-SP-Timestamp"
	HeaderKeyID     = "X-SP-Key-Id"
)

// SignaturePrefix versions the signature value so a v2 scheme can coexist.
const SignaturePrefix = "v1,"

// MaxSkew is how far the request timestamp may be from local time, in either
// direction. 300s, matching the billing side.
const MaxSkew = 300 * time.Second

// Verification failures. Callers log the specific reason and return one
// generic 401 — the reason never reaches the response body.
var (
	ErrNoSignature   = errors.New("no signature headers")
	ErrMalformed     = errors.New("malformed signature headers")
	ErrUnknownKeyID  = errors.New("unknown key id")
	ErrSkew          = errors.New("timestamp outside the accepted skew window")
	ErrBadSignature  = errors.New("signature mismatch")
	ErrNoKeysDefined = errors.New("no signing keys configured")
	// ErrInvalidKeySet reports a key set that could not be used as configured.
	ErrInvalidKeySet = errors.New("invalid signing key set")
)

// Key is one shared secret and the id the signer advertises for it.
type Key struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
}

// KeySet is an ordered set of keys, newest first. The first entry signs; any
// entry verifies.
type KeySet []Key

// Empty reports whether the set holds no usable key.
func (ks KeySet) Empty() bool { return len(ks) == 0 }

// Current returns the key new signatures are produced with.
func (ks KeySet) Current() (Key, bool) {
	if len(ks) == 0 {
		return Key{}, false
	}

	return ks[0], true
}

// find returns the key advertised under id.
func (ks KeySet) find(id string) (Key, bool) {
	for i := range ks {
		if ks[i].ID == id {
			return ks[i], true
		}
	}

	return Key{}, false
}

// ParseKeySet decodes the JSON array form stored in a system parameter:
//
//	[{"id":"2026-08","secret":"…"},{"id":"2026-05","secret":"…"}]
//
// Blank input yields an empty set with no error (the feature is simply off).
// Entries with an empty id or secret are rejected rather than silently
// dropped, so a typo cannot quietly disable verification.
func ParseKeySet(raw string) (KeySet, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	var keys KeySet
	if err := json.Unmarshal([]byte(raw), &keys); err != nil {
		return nil, fmt.Errorf("parsing signing key set: %w", err)
	}

	seen := make(map[string]bool, len(keys))

	for i := range keys {
		if keys[i].ID == "" || keys[i].Secret == "" {
			return nil, fmt.Errorf("%w: key #%d needs both an id and a secret", ErrInvalidKeySet, i)
		}

		if seen[keys[i].ID] {
			return nil, fmt.Errorf("%w: key #%d repeats id %q", ErrInvalidKeySet, i, keys[i].ID)
		}

		seen[keys[i].ID] = true
	}

	return keys, nil
}

// Canonical builds the string that gets signed. This is the wire contract —
// pinned by a test here and mirrored by a test in solidping-billing.
func Canonical(timestamp int64, method, path string, body []byte) string {
	sum := sha256.Sum256(body)

	return strconv.FormatInt(timestamp, 10) + "." +
		strings.ToUpper(method) + "." +
		path + "." +
		hex.EncodeToString(sum[:])
}

// Sign returns the base64 HMAC-SHA256 of the canonical string, without the
// version prefix.
func Sign(key Key, timestamp int64, method, path string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(key.Secret))
	mac.Write([]byte(Canonical(timestamp, method, path, body)))

	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// SignRequest sets the three signature headers on req using the current key of
// ks. body must be the exact bytes that will be sent (nil for an empty body).
// It never sets an Authorization header: signatures replace static bearers.
//
// The X-SP-* spelling is the cross-repo wire contract; net/http canonicalizes
// header names on both Set and Get, so the linter's preferred X-Sp-* form is
// the same header — the documented spelling is kept.
//
//nolint:canonicalheader // documented cross-repo header names, canonicalized by net/http anyway
func SignRequest(req *http.Request, keys KeySet, body []byte, now time.Time) error {
	key, ok := keys.Current()
	if !ok {
		return ErrNoKeysDefined
	}

	timestamp := now.Unix()
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(timestamp, 10))
	req.Header.Set(HeaderKeyID, key.ID)
	req.Header.Set(HeaderSignature, SignaturePrefix+Sign(key, timestamp, req.Method, req.URL.Path, body))

	return nil
}

// HasSignature reports whether the request carries a signature header at all.
// A request without one is left to the legacy bearer / user-auth path.
//
//nolint:canonicalheader // documented cross-repo header names, canonicalized by net/http anyway
func HasSignature(req *http.Request) bool {
	return req.Header.Get(HeaderSignature) != ""
}

// VerifyRequest verifies the signature headers of req against keys, for the
// given raw body. now is the reference time (injected for testability).
//
// Rejection order matches the spec: absent/unknown key id, then clock skew,
// then signature mismatch. The comparison is constant time.
//
//nolint:canonicalheader // documented cross-repo header names, canonicalized by net/http anyway
func VerifyRequest(req *http.Request, keys KeySet, body []byte, now time.Time) (Key, error) {
	return Verify(
		keys,
		req.Method, req.URL.Path, body,
		req.Header.Get(HeaderSignature),
		req.Header.Get(HeaderTimestamp),
		req.Header.Get(HeaderKeyID),
		now,
	)
}

// Verify is the header-agnostic core of VerifyRequest. It returns the key that
// validated the request, which callers may log for attribution.
//
//nolint:cyclop // a flat ordered sequence of rejection reasons is clearer than nesting
func Verify(
	keys KeySet,
	method, path string, body []byte,
	signature, timestamp, keyID string,
	now time.Time,
) (Key, error) {
	if signature == "" && timestamp == "" && keyID == "" {
		return Key{}, ErrNoSignature
	}

	if keys.Empty() {
		return Key{}, ErrNoKeysDefined
	}

	if signature == "" || timestamp == "" || keyID == "" {
		return Key{}, ErrMalformed
	}

	encoded, ok := strings.CutPrefix(signature, SignaturePrefix)
	if !ok {
		return Key{}, ErrMalformed
	}

	provided, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return Key{}, ErrMalformed
	}

	key, ok := keys.find(keyID)
	if !ok {
		return Key{}, ErrUnknownKeyID
	}

	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return Key{}, ErrMalformed
	}

	if delta := now.Sub(time.Unix(seconds, 0)); delta > MaxSkew || delta < -MaxSkew {
		return Key{}, ErrSkew
	}

	expected, err := base64.StdEncoding.DecodeString(Sign(key, seconds, method, path, body))
	if err != nil {
		return Key{}, ErrMalformed
	}

	// Constant-time: never compare signatures with ==.
	if !hmac.Equal(provided, expected) {
		return Key{}, ErrBadSignature
	}

	return key, nil
}

// SystemParamReader is the slice of the DB service this package needs. Kept as
// a local interface so servicesig stays importable from both the middleware
// and the handler layers without an import cycle.
type SystemParamReader interface {
	GetSystemParameter(ctx context.Context, key string) (*models.Parameter, error)
}

// LoadKeySet reads and parses an ordered key set from a system parameter.
// A missing or blank parameter yields an empty set and no error: signing and
// verification are simply not configured (self-hosted installs never are).
func LoadKeySet(ctx context.Context, reader SystemParamReader, paramKey string) (KeySet, error) {
	param, err := reader.GetSystemParameter(ctx, paramKey)
	if err != nil || param == nil {
		return nil, err
	}

	raw, _ := param.Value["value"].(string)

	return ParseKeySet(raw)
}

// Signer signs outbound requests to the billing service with the key set held
// in paramKey. It exists so that whoever writes the checkout/portal proxy
// client signs from the first commit instead of reaching for a static bearer.
type Signer struct {
	reader   SystemParamReader
	paramKey string
}

// NewSigner builds a Signer reading its keys from the given system parameter.
func NewSigner(reader SystemParamReader, paramKey string) *Signer {
	return &Signer{reader: reader, paramKey: paramKey}
}

// Sign stamps req with the signature headers. body must be the exact bytes of
// the request payload (nil when there is none). It returns ErrNoKeysDefined
// when no outbound key is configured, so a caller can decide whether that is
// fatal or merely "billing integration not set up".
func (s *Signer) Sign(ctx context.Context, req *http.Request, body []byte) error {
	keys, err := LoadKeySet(ctx, s.reader, s.paramKey)
	if err != nil {
		return err
	}

	return SignRequest(req, keys, body, time.Now())
}
