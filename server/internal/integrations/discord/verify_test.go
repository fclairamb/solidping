package discord

import (
	"crypto/ed25519"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

// signedRequest builds a request signed the way Discord signs one.
func signedRequest(t *testing.T, priv ed25519.PrivateKey, timestamp, body string) *http.Request {
	t.Helper()

	sig := ed25519.Sign(priv, []byte(timestamp+body))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"/api/v1/integrations/discord/interactions", strings.NewReader(body))
	req.Header.Set(TimestampHeader, timestamp)
	req.Header.Set(SignatureHeader, hex.EncodeToString(sig))

	return req
}

// verifyHandler wires the middleware in front of a handler that records
// whether it ran, and returns 200.
func verifyHandler(t *testing.T, publicKeyHex string) (*Handler, *bool) {
	t.Helper()

	reached := false

	cfg := &config.Config{Discord: config.DiscordOAuthConfig{PublicKey: publicKeyHex}}
	h := NewHandler(NewService(nil, cfg, nil, nil), cfg)

	return h, &reached
}

func TestVerifyMiddleware_AcceptsValidSignature(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	pub, priv, err := ed25519.GenerateKey(nil)
	r.NoError(err)

	h, reached := verifyHandler(t, hex.EncodeToString(pub))

	const body = `{"type":1}`

	var (
		downstreamBody string
		downstreamErr  error
	)

	handler := h.VerifyMiddleware(func(w http.ResponseWriter, req *http.Request) error {
		*reached = true

		// The body must still be readable downstream. Captured here and
		// asserted after the handler returns: a failed assertion inside an
		// HTTP handler aborts the wrong goroutine.
		raw, readErr := io.ReadAll(req.Body)
		downstreamBody, downstreamErr = string(raw), readErr

		w.WriteHeader(http.StatusOK)

		return nil
	})

	rec := httptest.NewRecorder()
	req := signedRequest(t, priv, strconv.FormatInt(time.Now().Unix(), 10), body)

	r.NoError(handler(rec, req))
	r.True(*reached)
	r.Equal(http.StatusOK, rec.Code)
	r.NoError(downstreamErr)
	r.Equal(body, downstreamBody)
}

// TestVerifyMiddleware_RejectsInvalidSignature is the one Discord probes with.
// It must be REJECTED — a 200 here gets the app's interactions endpoint
// deactivated by Discord.
func TestVerifyMiddleware_RejectsInvalidSignature(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	pub, _, err := ed25519.GenerateKey(nil)
	r.NoError(err)

	// A signature made with a DIFFERENT key — exactly Discord's probe.
	_, otherPriv, err := ed25519.GenerateKey(nil)
	r.NoError(err)

	h, reached := verifyHandler(t, hex.EncodeToString(pub))

	handler := h.VerifyMiddleware(func(w http.ResponseWriter, _ *http.Request) error {
		*reached = true
		w.WriteHeader(http.StatusOK)

		return nil
	})

	rec := httptest.NewRecorder()
	req := signedRequest(t, otherPriv, strconv.FormatInt(time.Now().Unix(), 10), `{"type":1}`)

	r.NoError(handler(rec, req))
	r.False(*reached, "an invalid signature must never reach the handler")
	r.Equal(http.StatusUnauthorized, rec.Code)
}

// TestVerifyMiddleware_RejectsTamperedBody: the signature covers timestamp+body,
// so changing the body after signing must fail.
func TestVerifyMiddleware_RejectsTamperedBody(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	pub, priv, err := ed25519.GenerateKey(nil)
	r.NoError(err)

	h, reached := verifyHandler(t, hex.EncodeToString(pub))

	handler := h.VerifyMiddleware(func(w http.ResponseWriter, _ *http.Request) error {
		*reached = true
		w.WriteHeader(http.StatusOK)

		return nil
	})

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	req := signedRequest(t, priv, timestamp, `{"type":3,"data":{"custom_id":"ack:mine"}}`)
	req.Body = io.NopCloser(strings.NewReader(`{"type":3,"data":{"custom_id":"ack:somebody-elses"}}`))

	rec := httptest.NewRecorder()

	r.NoError(handler(rec, req))
	r.False(*reached)
	r.Equal(http.StatusUnauthorized, rec.Code)
}

// TestVerifyMiddleware_RejectsStaleTimestamp bounds replay of a correctly
// signed but old request.
func TestVerifyMiddleware_RejectsStaleTimestamp(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	pub, priv, err := ed25519.GenerateKey(nil)
	r.NoError(err)

	h, reached := verifyHandler(t, hex.EncodeToString(pub))

	handler := h.VerifyMiddleware(func(w http.ResponseWriter, _ *http.Request) error {
		*reached = true
		w.WriteHeader(http.StatusOK)

		return nil
	})

	stale := strconv.FormatInt(time.Now().Add(-MaxTimestampAge-time.Minute).Unix(), 10)

	rec := httptest.NewRecorder()

	r.NoError(handler(rec, signedRequest(t, priv, stale, `{"type":1}`)))
	r.False(*reached, "a correctly signed but stale request must still be rejected")
	r.Equal(http.StatusUnauthorized, rec.Code)
}

// TestVerifyMiddleware_RejectsFarFutureTimestamp: a far-future timestamp would
// keep a captured request replayable for as long as the attacker chose.
func TestVerifyMiddleware_RejectsFarFutureTimestamp(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	pub, priv, err := ed25519.GenerateKey(nil)
	r.NoError(err)

	h, reached := verifyHandler(t, hex.EncodeToString(pub))

	handler := h.VerifyMiddleware(func(_ http.ResponseWriter, _ *http.Request) error {
		*reached = true

		return nil
	})

	future := strconv.FormatInt(time.Now().Add(24*time.Hour).Unix(), 10)

	rec := httptest.NewRecorder()

	r.NoError(handler(rec, signedRequest(t, priv, future, `{"type":1}`)))
	r.False(*reached)
	r.Equal(http.StatusUnauthorized, rec.Code)
}

func TestVerifyMiddleware_RejectsMissingHeaders(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	pub, _, err := ed25519.GenerateKey(nil)
	r.NoError(err)

	h, reached := verifyHandler(t, hex.EncodeToString(pub))

	handler := h.VerifyMiddleware(func(_ http.ResponseWriter, _ *http.Request) error {
		*reached = true

		return nil
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/x",
		strings.NewReader(`{"type":1}`))

	r.NoError(handler(rec, req))
	r.False(*reached)
	r.Equal(http.StatusUnauthorized, rec.Code)
}

// TestVerifyMiddleware_UnconfiguredKeyRejects is the deliberate divergence from
// the Slack middleware: with no public key we cannot verify anything, so we
// refuse everything rather than trusting everything.
func TestVerifyMiddleware_UnconfiguredKeyRejects(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for _, key := range []string{"", "not-hex", "abcd" /* right hex, wrong length */} {
		h, reached := verifyHandler(t, key)

		handler := h.VerifyMiddleware(func(_ http.ResponseWriter, _ *http.Request) error {
			*reached = true

			return nil
		})

		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/x",
			strings.NewReader(`{"type":1}`))
		req.Header.Set(TimestampHeader, strconv.FormatInt(time.Now().Unix(), 10))
		req.Header.Set(SignatureHeader, strings.Repeat("00", ed25519.SignatureSize))

		r.NoError(handler(rec, req))
		r.False(*reached, "public key %q must not enable the endpoint", key)
		r.Equal(http.StatusUnauthorized, rec.Code)
	}
}
