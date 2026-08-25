package discord

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

const (
	// SignatureHeader carries the hex-encoded Ed25519 signature.
	SignatureHeader = "X-Signature-Ed25519"
	// TimestampHeader carries the Unix timestamp the signature covers.
	TimestampHeader = "X-Signature-Timestamp"

	// MaxTimestampAge bounds replay of a captured, correctly-signed request.
	// Discord itself does not require this; we do, for the same reason Slack
	// does — a signature with no freshness bound is a bearer token that never
	// expires.
	MaxTimestampAge = 5 * time.Minute

	// maxInteractionBody caps how much of an inbound body we will read before
	// verifying it. Discord interaction payloads are a few KB at most.
	maxInteractionBody = 1 << 20
)

// VerifyMiddleware verifies Discord's Ed25519 request signature.
//
// Unlike the Slack middleware, this one does NOT fall through when no key is
// configured. Two reasons, and both are decisive:
//
//   - The interactions endpoint is what acknowledges and escalates incidents.
//     An unverified endpoint lets any caller on the internet acknowledge any
//     incident whose uid they can guess or read off a screenshot.
//   - Discord probes the endpoint with deliberately INVALID signatures during
//     app setup and periodically afterwards, and DEACTIVATES the endpoint if a
//     probe is answered with anything other than 401. An implementation that
//     "skips verification when unconfigured" answers those probes 200 and gets
//     the app's interactions turned off.
//
// So a missing public key means "reject everything", not "trust everything".
func (h *Handler) VerifyMiddleware(next httpx.HandlerFunc) httpx.HandlerFunc {
	return func(writer http.ResponseWriter, req *http.Request) error {
		publicKey := ""
		if h.cfg != nil {
			publicKey = h.cfg.Discord.PublicKey
		}

		key, ok := decodePublicKey(publicKey)
		if !ok {
			return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized,
				"Discord signature verification is not configured")
		}

		timestamp := req.Header.Get(TimestampHeader)
		signature := req.Header.Get(SignatureHeader)

		if timestamp == "" || signature == "" {
			return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized,
				"Missing Discord signature headers")
		}

		if !timestampFresh(timestamp, time.Now()) {
			return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized,
				"Request timestamp is invalid or too old")
		}

		body, err := io.ReadAll(io.LimitReader(req.Body, maxInteractionBody))
		if err != nil {
			return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
				"Failed to read request body")
		}

		// Restore the body for the downstream handler.
		req.Body = io.NopCloser(bytes.NewReader(body))

		if !VerifySignature(key, timestamp, body, signature) {
			return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized,
				"Invalid Discord signature")
		}

		return next(writer, req)
	}
}

// decodePublicKey decodes the configured hex public key, rejecting anything
// that is not a well-formed Ed25519 key.
func decodePublicKey(hexKey string) (ed25519.PublicKey, bool) {
	if hexKey == "" {
		return nil, false
	}

	raw, err := hex.DecodeString(hexKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, false
	}

	return raw, true
}

// timestampFresh reports whether the signed timestamp is a plausible, recent
// Unix second count. A timestamp far in the FUTURE is rejected too: it would
// otherwise let a captured request stay replayable indefinitely.
func timestampFresh(timestamp string, now time.Time) bool {
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}

	age := now.Sub(time.Unix(seconds, 0))
	if age < 0 {
		age = -age
	}

	return age <= MaxTimestampAge
}

// VerifySignature checks Discord's Ed25519 signature over `timestamp + body`.
// Exported so the Gateway/testing paths can reuse exactly the same check.
func VerifySignature(key ed25519.PublicKey, timestamp string, body []byte, hexSignature string) bool {
	signature, err := hex.DecodeString(hexSignature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return false
	}

	message := make([]byte, 0, len(timestamp)+len(body))
	message = append(message, timestamp...)
	message = append(message, body...)

	return ed25519.Verify(key, message, signature)
}
