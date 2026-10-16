package slack

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

const (
	// SlackSignatureHeader is the header containing the Slack signature.
	SlackSignatureHeader = "X-Slack-Signature"
	// SlackTimestampHeader is the header containing the request timestamp.
	SlackTimestampHeader = "X-Slack-Request-Timestamp"
	// SignatureVersion is the version prefix for Slack signatures.
	SignatureVersion = "v0"
	// MaxTimestampAge is the maximum age of a request timestamp (5 minutes),
	// in seconds. It bounds both directions: a timestamp that far in the
	// past or in the future is rejected.
	MaxTimestampAge = 5 * 60

	// maxSlackBody caps how much of an inbound body is read before its
	// signature is checked. Slack payloads are a few KB at most; the cap
	// matches Discord's maxInteractionBody.
	maxSlackBody = 1 << 20
)

// VerifyMiddleware verifies Slack's HMAC-SHA256 request signature.
//
// It does NOT fall through when no signing secret is configured. The three
// HTTP endpoints it guards (events, slash commands, interactions) read and
// change monitoring data on behalf of whichever workspace the payload names:
// list checks and incidents, create and delete checks, acknowledge and
// escalate incidents. A `team_id` is not a secret, so an unverified endpoint
// lets any caller on the internet act as any linked workspace, and a slash
// command's caller-supplied response_url turns it into an outbound request
// primitive.
//
// So a missing signing secret means "reject everything", not "trust
// everything". Socket Mode installs are unaffected: their events arrive over
// the WebSocket opened with the xapp- token and are dispatched without going
// through this middleware, and Slack never calls the HTTP Request URLs of an
// app that has Socket Mode turned on.
func (h *Handler) VerifyMiddleware(next httpx.HandlerFunc) httpx.HandlerFunc {
	return func(writer http.ResponseWriter, req *http.Request) error {
		signingSecret := ""
		if h.cfg != nil {
			signingSecret = h.cfg.Slack.SigningSecret
		}

		if signingSecret == "" {
			return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized,
				"Slack signature verification is not configured")
		}

		timestamp := req.Header.Get(SlackTimestampHeader)
		if timestamp == "" {
			return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized,
				"Missing Slack timestamp header")
		}

		if !timestampFresh(timestamp, time.Now()) {
			return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized,
				"Request timestamp is invalid or too old")
		}

		signature := req.Header.Get(SlackSignatureHeader)
		if signature == "" {
			return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized,
				"Missing Slack signature header")
		}

		// Cap the body before reading it: the signature is only checked
		// once the whole body is in memory.
		body, err := io.ReadAll(http.MaxBytesReader(writer, req.Body, maxSlackBody))
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				return h.WriteError(writer, http.StatusRequestEntityTooLarge, base.ErrorCodeValidationError,
					"Request body too large")
			}

			return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
				"Failed to read request body")
		}

		// Restore body for downstream handlers
		req.Body = io.NopCloser(bytes.NewReader(body))

		if !verifySignature(signingSecret, timestamp, body, signature) {
			return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized,
				"Invalid Slack signature")
		}

		return next(writer, req)
	}
}

// timestampFresh reports whether the signed timestamp is a plausible, recent
// Unix second count. A timestamp far in the FUTURE is rejected too: it would
// otherwise let a captured request stay replayable indefinitely.
func timestampFresh(timestamp string, now time.Time) bool {
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}

	// Compared on both sides rather than through an absolute value: negating
	// the most negative int64 overflows back to itself.
	age := now.Unix() - seconds

	return age >= -MaxTimestampAge && age <= MaxTimestampAge
}

// verifySignature computes and verifies the Slack request signature.
func verifySignature(signingSecret, timestamp string, body []byte, expectedSignature string) bool {
	// Create the base string: v0:timestamp:body
	baseString := fmt.Sprintf("%s:%s:%s", SignatureVersion, timestamp, string(body))

	// Compute HMAC-SHA256
	mac := hmac.New(sha256.New, []byte(signingSecret))
	mac.Write([]byte(baseString))
	computedSignature := SignatureVersion + "=" + hex.EncodeToString(mac.Sum(nil))

	// Compare signatures using constant-time comparison
	return hmac.Equal([]byte(computedSignature), []byte(expectedSignature))
}
