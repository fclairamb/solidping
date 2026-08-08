package servicesig_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/servicesig"
)

const (
	testPath   = "/api/v1/orgs/acme/entitlements"
	testKeyID  = "k-2026-08"
	testSecret = "s3cr3t-inbound"
)

func testKeys() servicesig.KeySet {
	return servicesig.KeySet{{ID: testKeyID, Secret: testSecret}}
}

// TestCanonicalPinsTheWireContract pins the exact bytes that get signed. This
// string is a cross-repo contract with solidping-billing: if this test needs
// changing, the billing side must change in the same release.
func TestCanonicalPinsTheWireContract(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	got := servicesig.Canonical(1754640000, "PUT", testPath, []byte(`{"maxChecks":100}`))

	// Fully literal: timestamp, uppercase method, path, and the hex sha256 of
	// the exact body bytes, joined by dots. The digest is
	// `printf '%s' '{"maxChecks":100}' | shasum -a 256`.
	r.Equal(
		"1754640000.PUT./api/v1/orgs/acme/entitlements."+
			"284ddd14e888344b03e33d925730b9435e208e309bbc65c21fed127e47add019",
		got,
		"canonical string must be <timestamp>.<METHOD>.<path>.<hex sha256 body>",
	)

	// The empty body hashes the empty string, not the literal "null".
	r.Equal(
		"1754640000.GET./p."+
			"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		servicesig.Canonical(1754640000, "GET", "/p", nil),
	)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)

	return hex.EncodeToString(sum[:])
}

// TestCanonicalUppercasesMethod guards the one normalization in the contract.
func TestCanonicalUppercasesMethod(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(
		servicesig.Canonical(1, "PUT", "/p", nil),
		servicesig.Canonical(1, "put", "/p", nil),
	)
}

// TestSignMatchesAnIndependentHMAC recomputes the HMAC by hand so the test
// does not merely assert Sign against itself.
func TestSignMatchesAnIndependentHMAC(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	body := []byte(`{"maxChecks":7}`)
	ts := int64(1754640123)

	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte(strconv.FormatInt(ts, 10) + ".PUT." + testPath + "." + sha256Hex(body)))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	r.Equal(want, servicesig.Sign(servicesig.Key{ID: testKeyID, Secret: testSecret}, ts, "PUT", testPath, body))
}

func signedRequest(t *testing.T, keys servicesig.KeySet, body []byte, now time.Time) *http.Request {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, testPath, nil)
	require.NoError(t, servicesig.SignRequest(req, keys, body, now))

	return req
}

// TestVerifyAcceptsAValidSignature is the positive control that every negative
// test below is a one-field mutation of.
func TestVerifyAcceptsAValidSignature(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Now()
	body := []byte(`{"maxChecks":100}`)
	req := signedRequest(t, testKeys(), body, now)

	r.Equal("v1,", req.Header.Get(servicesig.HeaderSignature)[:3])
	r.Equal(testKeyID, req.Header.Get(servicesig.HeaderKeyID))
	r.NotEmpty(req.Header.Get(servicesig.HeaderTimestamp))

	key, err := servicesig.VerifyRequest(req, testKeys(), body, now)
	r.NoError(err)
	r.Equal(testKeyID, key.ID)
}

func TestVerifyRejectsATamperedBody(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Now()
	req := signedRequest(t, testKeys(), []byte(`{"maxChecks":100}`), now)

	// One byte changed: 100 -> 900.
	_, err := servicesig.VerifyRequest(req, testKeys(), []byte(`{"maxChecks":900}`), now)
	r.ErrorIs(err, servicesig.ErrBadSignature)
}

func TestVerifyRejectsATamperedMethodOrPath(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Now()
	body := []byte(`{}`)
	req := signedRequest(t, testKeys(), body, now)

	// Replay the same headers against a different org's path.
	replay := httptest.NewRequestWithContext(
		t.Context(), http.MethodPut, "/api/v1/orgs/victim/entitlements", nil)
	replay.Header = req.Header.Clone()
	_, err := servicesig.VerifyRequest(replay, testKeys(), body, now)
	r.ErrorIs(err, servicesig.ErrBadSignature)

	// Same path, different method.
	replay2 := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, testPath, nil)
	replay2.Header = req.Header.Clone()
	_, err = servicesig.VerifyRequest(replay2, testKeys(), body, now)
	r.ErrorIs(err, servicesig.ErrBadSignature)
}

func TestVerifyRejectsStaleAndFutureTimestamps(t *testing.T) {
	t.Parallel()

	for name, offset := range map[string]time.Duration{
		"too old":    -servicesig.MaxSkew - time.Second,
		"too future": servicesig.MaxSkew + time.Second,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			now := time.Now()
			body := []byte(`{}`)
			req := signedRequest(t, testKeys(), body, now.Add(offset))

			_, err := servicesig.VerifyRequest(req, testKeys(), body, now)
			r.ErrorIs(err, servicesig.ErrSkew)

			// Positive control: just inside the window is accepted.
			inside := signedRequest(t, testKeys(), body, now.Add(offset/2))
			_, err = servicesig.VerifyRequest(inside, testKeys(), body, now)
			r.NoError(err)
		})
	}
}

func TestVerifyRejectsAnUnknownKeyID(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Now()
	body := []byte(`{}`)
	req := signedRequest(t, servicesig.KeySet{{ID: "rogue", Secret: testSecret}}, body, now)

	_, err := servicesig.VerifyRequest(req, testKeys(), body, now)
	r.ErrorIs(err, servicesig.ErrUnknownKeyID)
}

// TestVerifyRotationAcceptsEitherKey is the rotation acceptance criterion.
func TestVerifyRotationAcceptsEitherKey(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Now()
	body := []byte(`{"maxChecks":5}`)
	newKey := servicesig.Key{ID: "k-new", Secret: "new-secret"}
	oldKey := servicesig.Key{ID: "k-old", Secret: "old-secret"}
	verifier := servicesig.KeySet{newKey, oldKey}

	// Signing uses the first (newest) key.
	signed := signedRequest(t, verifier, body, now)
	r.Equal("k-new", signed.Header.Get(servicesig.HeaderKeyID))

	for _, key := range []servicesig.Key{newKey, oldKey} {
		req := signedRequest(t, servicesig.KeySet{key}, body, now)
		got, err := servicesig.VerifyRequest(req, verifier, body, now)
		r.NoError(err, "key %s must verify during rotation", key.ID)
		r.Equal(key.ID, got.ID)
	}

	// A key that is *not* in the set still fails — the set is not a free pass.
	stranger := signedRequest(t, servicesig.KeySet{{ID: "k-new", Secret: "wrong"}}, body, now)
	_, err := servicesig.VerifyRequest(stranger, verifier, body, now)
	r.ErrorIs(err, servicesig.ErrBadSignature)
}

func TestVerifyRejectsMalformedHeaders(t *testing.T) {
	t.Parallel()

	cases := map[string]func(h http.Header){
		"missing prefix":     func(h http.Header) { h.Set(servicesig.HeaderSignature, "abcd") },
		"wrong version":      func(h http.Header) { h.Set(servicesig.HeaderSignature, "v2,abcd") },
		"not base64":         func(h http.Header) { h.Set(servicesig.HeaderSignature, "v1,!!!not-base64!!!") },
		"no timestamp":       func(h http.Header) { h.Del(servicesig.HeaderTimestamp) },
		"no key id":          func(h http.Header) { h.Del(servicesig.HeaderKeyID) },
		"timestamp not int":  func(h http.Header) { h.Set(servicesig.HeaderTimestamp, "yesterday") },
		"empty signature v1": func(h http.Header) { h.Set(servicesig.HeaderSignature, "v1,") },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			now := time.Now()
			body := []byte(`{}`)
			req := signedRequest(t, testKeys(), body, now)

			// Positive control on the untouched request.
			_, err := servicesig.VerifyRequest(req, testKeys(), body, now)
			r.NoError(err)

			mutate(req.Header)
			_, err = servicesig.VerifyRequest(req, testKeys(), body, now)
			r.Error(err)
			r.NotErrorIs(err, servicesig.ErrNoSignature)
		})
	}
}

func TestVerifyWithNoHeadersAtAll(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, testPath, nil)
	_, err := servicesig.VerifyRequest(req, testKeys(), nil, time.Now())
	r.ErrorIs(err, servicesig.ErrNoSignature)
	r.False(servicesig.HasSignature(req))
}

func TestVerifyWithNoKeysConfigured(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Now()
	req := signedRequest(t, testKeys(), nil, now)

	_, err := servicesig.VerifyRequest(req, nil, nil, now)
	r.ErrorIs(err, servicesig.ErrNoKeysDefined)
}

func TestSignRequestWithoutKeys(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, testPath, nil)
	r.ErrorIs(servicesig.SignRequest(req, nil, nil, time.Now()), servicesig.ErrNoKeysDefined)
	r.Empty(req.Header.Get(servicesig.HeaderSignature))
}

// TestSignRequestNeverSetsAuthorization — signatures replace static bearers;
// the signer must not reintroduce one.
func TestSignRequestNeverSetsAuthorization(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	req := signedRequest(t, testKeys(), []byte(`{}`), time.Now())
	r.Empty(req.Header.Get("Authorization"))
}

func TestParseKeySet(t *testing.T) {
	t.Parallel()

	t.Run("ordered set", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		keys, err := servicesig.ParseKeySet(`[{"id":"a","secret":"1"},{"id":"b","secret":"2"}]`)
		r.NoError(err)
		r.Len(keys, 2)

		current, ok := keys.Current()
		r.True(ok)
		r.Equal("a", current.ID, "the first entry is the signing key")
	})

	t.Run("blank is off, not an error", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		for _, raw := range []string{"", "   ", "\n"} {
			keys, err := servicesig.ParseKeySet(raw)
			r.NoError(err)
			r.True(keys.Empty())
		}
	})

	t.Run("rejects malformed entries", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		for _, raw := range []string{
			`not json`,
			`[{"id":"","secret":"x"}]`,
			`[{"id":"x","secret":""}]`,
			`[{"id":"dup","secret":"1"},{"id":"dup","secret":"2"}]`,
		} {
			_, err := servicesig.ParseKeySet(raw)
			r.Error(err, "input %q must be rejected", raw)
		}
	})
}

// fakeParams is a SystemParamReader backed by a map, so the loader/signer can
// be tested without a database.
type fakeParams struct {
	values map[string]any
	err    error
}

func (f fakeParams) GetSystemParameter(_ context.Context, key string) (*models.Parameter, error) {
	if f.err != nil {
		return nil, f.err
	}

	value, ok := f.values[key]
	if !ok {
		return nil, nil //nolint:nilnil // absent parameter, matching db.Service
	}

	return &models.Parameter{Key: key, Value: models.JSONMap{"value": value}}, nil
}

func TestLoadKeySet(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	reader := fakeParams{values: map[string]any{
		"ok":      `[{"id":"a","secret":"1"}]`,
		"bad":     `[{"id":"a"}]`,
		"notastr": 42,
	}}

	keys, err := servicesig.LoadKeySet(ctx, reader, "ok")
	r.NoError(err)
	r.Len(keys, 1)

	_, err = servicesig.LoadKeySet(ctx, reader, "bad")
	r.Error(err)

	// Absent parameter and a non-string value both mean "not configured".
	for _, key := range []string{"absent", "notastr"} {
		keys, err = servicesig.LoadKeySet(ctx, reader, key)
		r.NoError(err)
		r.True(keys.Empty())
	}

	_, err = servicesig.LoadKeySet(ctx, fakeParams{err: errBoom}, "ok")
	r.ErrorIs(err, errBoom)
}

var errBoom = errors.New("boom")

// TestSignerSignsOutboundRequests covers the OSS -> billing direction: the
// helper that exists so the future checkout/portal proxy client is signed from
// its first commit instead of carrying a static bearer.
func TestSignerSignsOutboundRequests(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	const paramKey = "entitlements.outbound_signing_keys"

	outbound := servicesig.Key{ID: "out-1", Secret: "outbound-secret"}
	reader := fakeParams{values: map[string]any{
		paramKey: `[{"id":"out-1","secret":"outbound-secret"}]`,
	}}
	signer := servicesig.NewSigner(reader, paramKey)

	body := []byte(`{"org":"acme"}`)
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/checkout", nil)
	r.NoError(signer.Sign(ctx, req, body))

	// The billing service verifies with the same key set.
	key, err := servicesig.VerifyRequest(req, servicesig.KeySet{outbound}, body, time.Now())
	r.NoError(err)
	r.Equal("out-1", key.ID)

	// The inbound key set must NOT validate an outbound-signed request: one
	// key set per direction is the whole point.
	_, err = servicesig.VerifyRequest(req, testKeys(), body, time.Now())
	r.ErrorIs(err, servicesig.ErrUnknownKeyID)

	// No outbound keys configured -> a clear error, not a silent unsigned call.
	empty := servicesig.NewSigner(fakeParams{values: map[string]any{}}, paramKey)
	bare := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/checkout", nil)
	r.ErrorIs(empty.Sign(ctx, bare, body), servicesig.ErrNoKeysDefined)
	r.Empty(bare.Header.Get(servicesig.HeaderSignature))
}
