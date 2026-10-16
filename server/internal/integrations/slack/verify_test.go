package slack

import (
	"crypto/hmac"
	"crypto/sha256"
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

const testSigningSecret = "test-signing-secret"

// slackSignature computes v0=HMAC-SHA256(secret, "v0:"+ts+":"+body).
func slackSignature(secret, timestamp, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + timestamp + ":" + body))

	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

// signedRequest builds a request signed the way Slack signs one.
func signedRequest(t *testing.T, secret, timestamp, body string) *http.Request {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"/api/v1/integrations/slack/command", strings.NewReader(body))
	req.Header.Set(SlackTimestampHeader, timestamp)
	req.Header.Set(SlackSignatureHeader, slackSignature(secret, timestamp, body))

	return req
}

func nowTimestamp(offset time.Duration) string {
	return strconv.FormatInt(time.Now().Add(offset).Unix(), 10)
}

// verifyResult is what one request through the middleware produced.
type verifyResult struct {
	status         int
	reached        bool
	downstreamBody string
}

// runVerify sends req through h.VerifyMiddleware in front of a handler that
// records whether it ran and what body it read.
func runVerify(t *testing.T, h *Handler, req *http.Request) verifyResult {
	t.Helper()

	var res verifyResult

	handler := h.VerifyMiddleware(func(w http.ResponseWriter, req *http.Request) error {
		res.reached = true

		// Captured here and asserted by the caller: a failed assertion
		// inside an HTTP handler aborts the wrong goroutine.
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return err
		}

		res.downstreamBody = string(raw)

		w.WriteHeader(http.StatusOK)

		return nil
	})

	rec := httptest.NewRecorder()
	require.NoError(t, handler(rec, req))

	res.status = rec.Code

	return res
}

func handlerWithSecret(secret string) *Handler {
	cfg := &config.Config{Slack: config.SlackConfig{SigningSecret: secret}}

	return NewHandler(nil, cfg)
}

func TestVerifyMiddleware(t *testing.T) {
	t.Parallel()

	const body = "command=%2Fsolidping&text=checks+list&team_id=T0"

	for _, tc := range []struct {
		name    string
		handler func() *Handler
		request func(t *testing.T) *http.Request
		status  int
	}{
		{
			// Positive control for every rejection below.
			name:    "valid signature reaches next",
			handler: func() *Handler { return handlerWithSecret(testSigningSecret) },
			request: func(t *testing.T) *http.Request {
				t.Helper()

				return signedRequest(t, testSigningSecret, nowTimestamp(0), body)
			},
			status: http.StatusOK,
		},
		{
			// Regression: this used to fall through to next.
			name:    "empty signing secret fails closed",
			handler: func() *Handler { return handlerWithSecret("") },
			request: func(t *testing.T) *http.Request {
				t.Helper()

				return signedRequest(t, "", nowTimestamp(0), body)
			},
			status: http.StatusUnauthorized,
		},
		{
			// DispatchCommand builds a Handler with no cfg.
			name:    "nil config fails closed",
			handler: func() *Handler { return &Handler{} },
			request: func(t *testing.T) *http.Request {
				t.Helper()

				return signedRequest(t, testSigningSecret, nowTimestamp(0), body)
			},
			status: http.StatusUnauthorized,
		},
		{
			name:    "wrong secret",
			handler: func() *Handler { return handlerWithSecret(testSigningSecret) },
			request: func(t *testing.T) *http.Request {
				t.Helper()

				return signedRequest(t, "another-secret", nowTimestamp(0), body)
			},
			status: http.StatusUnauthorized,
		},
		{
			name:    "tampered body",
			handler: func() *Handler { return handlerWithSecret(testSigningSecret) },
			request: func(t *testing.T) *http.Request {
				t.Helper()

				ts := nowTimestamp(0)
				req := signedRequest(t, testSigningSecret, ts, body)
				req.Body = io.NopCloser(strings.NewReader(body + "&text=checks+rm"))

				return req
			},
			status: http.StatusUnauthorized,
		},
		{
			name:    "missing timestamp",
			handler: func() *Handler { return handlerWithSecret(testSigningSecret) },
			request: func(t *testing.T) *http.Request {
				t.Helper()

				req := signedRequest(t, testSigningSecret, nowTimestamp(0), body)
				req.Header.Del(SlackTimestampHeader)

				return req
			},
			status: http.StatusUnauthorized,
		},
		{
			name:    "missing signature",
			handler: func() *Handler { return handlerWithSecret(testSigningSecret) },
			request: func(t *testing.T) *http.Request {
				t.Helper()

				req := signedRequest(t, testSigningSecret, nowTimestamp(0), body)
				req.Header.Del(SlackSignatureHeader)

				return req
			},
			status: http.StatusUnauthorized,
		},
		{
			name:    "stale timestamp",
			handler: func() *Handler { return handlerWithSecret(testSigningSecret) },
			request: func(t *testing.T) *http.Request {
				t.Helper()

				return signedRequest(t, testSigningSecret, nowTimestamp(-301*time.Second), body)
			},
			status: http.StatusUnauthorized,
		},
		{
			// Regression: the freshness check used to be one-sided.
			name:    "far-future timestamp",
			handler: func() *Handler { return handlerWithSecret(testSigningSecret) },
			request: func(t *testing.T) *http.Request {
				t.Helper()

				return signedRequest(t, testSigningSecret, nowTimestamp(301*time.Second), body)
			},
			status: http.StatusUnauthorized,
		},
		{
			name:    "non-numeric timestamp",
			handler: func() *Handler { return handlerWithSecret(testSigningSecret) },
			request: func(t *testing.T) *http.Request {
				t.Helper()

				return signedRequest(t, testSigningSecret, "yesterday", body)
			},
			status: http.StatusUnauthorized,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			res := runVerify(t, tc.handler(), tc.request(t))
			r.Equal(tc.status, res.status)

			if tc.status == http.StatusOK {
				r.True(res.reached)
				r.Equal(body, res.downstreamBody, "the downstream handler must read the identical body")
			} else {
				r.False(res.reached, "next must not run on a rejected request")
			}
		})
	}
}

func TestVerifyMiddleware_BodyCap(t *testing.T) {
	t.Parallel()

	t.Run("one byte over the cap is 413", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		body := strings.Repeat("a", maxSlackBody+1)
		res := runVerify(t, handlerWithSecret(testSigningSecret),
			signedRequest(t, testSigningSecret, nowTimestamp(0), body))

		r.Equal(http.StatusRequestEntityTooLarge, res.status)
		r.False(res.reached)
	})

	t.Run("exactly the cap passes", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		body := strings.Repeat("a", maxSlackBody)
		res := runVerify(t, handlerWithSecret(testSigningSecret),
			signedRequest(t, testSigningSecret, nowTimestamp(0), body))

		r.Equal(http.StatusOK, res.status)
		r.True(res.reached)
		r.Len(res.downstreamBody, maxSlackBody)
	})
}

func TestTimestampFresh(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)

	for _, tc := range []struct {
		name  string
		ts    string
		fresh bool
	}{
		{name: "now", ts: "1700000000", fresh: true},
		{name: "300s old", ts: "1699999700", fresh: true},
		{name: "300s ahead", ts: "1700000300", fresh: true},
		{name: "301s old", ts: "1699999699"},
		{name: "301s ahead", ts: "1700000301"},
		// now - ts overflows to math.MinInt64, whose absolute value would
		// overflow back to itself.
		{name: "overflowing difference", ts: strconv.FormatInt(1_700_000_000-(1<<63-1)-1, 10)},
		{name: "garbage", ts: "abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.fresh, timestampFresh(tc.ts, now))
		})
	}
}
