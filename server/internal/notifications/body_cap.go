package notifications

import (
	"errors"
	"io"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// senderMaxResponseBody caps how much of a response a sender buffers. The
// senders only need a small JSON answer or an error snippet, and the host is
// often one the user picks, so an uncapped read lets that host fill the
// server's memory (spec 2026-10-20-01). It matches the webhook sender's
// stored body cap.
const senderMaxResponseBody = models.DeliveryDetailsBodyCap

// senderErrorSnippet caps how much of a response body goes into an error
// string, matching discordWebhookBodySnippet.
const senderErrorSnippet = 512

// ErrSenderResponseTooLarge is returned when a sender needs to parse a
// response that goes over senderMaxResponseBody.
var ErrSenderResponseTooLarge = errors.New("response body too large")

// readCappedBody reads at most limit bytes of body. truncated reports that
// the body had more than limit bytes; the rest is never read.
func readCappedBody(body io.Reader, limit int64) (data []byte, truncated bool) {
	data, _ = io.ReadAll(io.LimitReader(body, limit+1))
	if int64(len(data)) > limit {
		return data[:limit], true
	}

	return data, false
}

// bodySnippet returns body as a string short enough for an error message.
func bodySnippet(body []byte) string {
	if len(body) <= senderErrorSnippet {
		return string(body)
	}

	return string(body[:senderErrorSnippet]) + "…[truncated]"
}
