package slack

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

const (
	// SlackSignatureHeader is the header containing the Slack signature.
	SlackSignatureHeader = "X-Slack-Signature"
	// SlackTimestampHeader is the header containing the request timestamp.
	SlackTimestampHeader = "X-Slack-Request-Timestamp"
	// SignatureVersion is the version prefix for Slack signatures.
	SignatureVersion = "v0"
	// MaxTimestampAge is the maximum age of a request timestamp (5 minutes).
	MaxTimestampAge = 5 * 60
)

// VerifyMiddleware creates a middleware that verifies Slack request signatures.
func (h *Handler) VerifyMiddleware(next bunrouter.HandlerFunc) bunrouter.HandlerFunc {
	return func(writer http.ResponseWriter, req bunrouter.Request) error {
		// Get the signing secret from config
		signingSecret := h.cfg.Slack.SigningSecret
		if signingSecret == "" {
			// If no signing secret is configured, skip verification
			return next(writer, req)
		}

		// Get timestamp from header
		timestamp := req.Header.Get(SlackTimestampHeader)
		if timestamp == "" {
			return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized,
				"Missing Slack timestamp header")
		}

		// Validate timestamp is not too old (prevent replay attacks)
		timestampInt, err := strconv.ParseInt(timestamp, 10, 64)
		if err != nil {
			return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized,
				"Invalid timestamp format")
		}

		if time.Now().Unix()-timestampInt > MaxTimestampAge {
			return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized,
				"Request timestamp too old")
		}

		// Get signature from header
		signature := req.Header.Get(SlackSignatureHeader)
		if signature == "" {
			return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized,
				"Missing Slack signature header")
		}

		// Read body
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
				"Failed to read request body")
		}

		// Restore body for downstream handlers
		req.Body = io.NopCloser(bytes.NewReader(body))

		// Verify signature
		if !verifySignature(signingSecret, timestamp, body, signature) {
			return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized,
				"Invalid Slack signature")
		}

		return next(writer, req)
	}
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
