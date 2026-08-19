package emailcheck

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jmap"
)

const testToken = "feedfacefeedfacefeedfacefeedfacefeedfacefeedface"

func TestExtractTokenAndStatusPlain(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := &Handler{}

	email := jmap.Email{
		To: []jmap.EmailAddress{{Email: testToken + "@inbox.example.com"}},
	}

	token, status, _ := h.extractTokenAndStatus(&email)
	r.Equal(testToken, token)
	r.Equal(statusUp, status)
}

func TestExtractTokenAndStatusPlusAddressing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := &Handler{}

	email := jmap.Email{
		To: []jmap.EmailAddress{{Email: testToken + "+down@inbox.example.com"}},
	}

	token, status, _ := h.extractTokenAndStatus(&email)
	r.Equal(testToken, token)
	r.Equal(statusDown, status)
}

func TestExtractTokenAndStatusHeaderOverridesDefault(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := &Handler{}

	email := jmap.Email{
		To:      []jmap.EmailAddress{{Email: testToken + "@inbox.example.com"}},
		Headers: []jmap.EmailHeader{{Name: "X-SolidPing-Status", Value: "error"}},
	}

	token, status, _ := h.extractTokenAndStatus(&email)
	r.Equal(testToken, token)
	r.Equal(statusError, status)
}

func TestExtractTokenAndStatusPlusBeatsHeader(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := &Handler{}

	email := jmap.Email{
		To:      []jmap.EmailAddress{{Email: testToken + "+running@inbox.example.com"}},
		Headers: []jmap.EmailHeader{{Name: "X-SolidPing-Status", Value: "down"}},
	}

	token, status, _ := h.extractTokenAndStatus(&email)
	r.Equal(testToken, token)
	r.Equal(statusRunning, status, "plus-addressing should win over header")
}

func TestExtractTokenAndStatusSubjectFallback(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := &Handler{}

	email := jmap.Email{
		To:      []jmap.EmailAddress{{Email: testToken + "@inbox.example.com"}},
		Subject: "[DOWN] disk full on prod-1",
	}

	token, status, _ := h.extractTokenAndStatus(&email)
	r.Equal(testToken, token)
	r.Equal(statusDown, status)
}

func TestExtractTokenAndStatusHeaderBeatsSubject(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := &Handler{}

	email := jmap.Email{
		To:      []jmap.EmailAddress{{Email: testToken + "@inbox.example.com"}},
		Headers: []jmap.EmailHeader{{Name: "X-SolidPing-Status", Value: "error"}},
		Subject: "[DOWN] but the header says error",
	}

	token, status, _ := h.extractTokenAndStatus(&email)
	r.Equal(testToken, token)
	r.Equal(statusError, status, "header beats subject")
}

func TestExtractTokenAndStatusNoToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := &Handler{}

	email := jmap.Email{
		To: []jmap.EmailAddress{{Email: "alice@example.com"}},
	}

	token, _, _ := h.extractTokenAndStatus(&email)
	r.Empty(token, "non-token recipient should not match")
}

func TestExtractTokenAndStatusCaseInsensitiveLocalPart(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := &Handler{}

	upper := "FEEDFACEFEEDFACEFEEDFACEFEEDFACEFEEDFACEFEEDFACE"
	email := jmap.Email{
		To: []jmap.EmailAddress{{Email: upper + "@inbox.example.com"}},
	}

	token, _, _ := h.extractTokenAndStatus(&email)
	r.Equal(testToken, token, "uppercase recipient should still resolve to lowercase token")
}

func TestExtractTokenScansAllRecipients(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := &Handler{}

	email := jmap.Email{
		To: []jmap.EmailAddress{{Email: "noise@example.com"}},
		Cc: []jmap.EmailAddress{{Email: testToken + "+down@inbox.example.com"}},
	}

	token, status, _ := h.extractTokenAndStatus(&email)
	r.Equal(testToken, token)
	r.Equal(statusDown, status)
}

func TestNormalizeStatus(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for _, raw := range []string{"up", "UP", "  down  ", "Error", "Running"} {
		_, ok := normalizeStatus(raw)
		r.True(ok, raw)
	}

	_, ok := normalizeStatus("paused")
	r.False(ok)
}

func TestDefaultMessage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal("Email received", defaultMessage(statusUp))
	r.Equal("Email reported failure", defaultMessage(statusDown))
	r.Equal("Email reported error", defaultMessage(statusError))
	r.Equal("Run started via email", defaultMessage(statusRunning))
}

func TestEnrichFromSMTPHeadersPresent(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	sentAt := time.Now().Add(-2 * time.Second).Truncate(time.Second)
	receivedAt := sentAt.Add(1500 * time.Millisecond)

	email := jmap.Email{
		ReceivedAt: receivedAt,
		Headers: []jmap.EmailHeader{
			{Name: headerSourceCheckUID, Value: "sending-check-uid"},
			{Name: headerSentAt, Value: sentAt.Format(time.RFC3339)},
		},
	}

	output := models.JSONMap{}
	metrics := models.JSONMap{}
	enrichFromSMTPHeaders(output, metrics, &email, "org-uid")

	r.Equal("sending-check-uid", output[outputKeySourceCheckUID])
	r.Equal(sentAt.Format(time.RFC3339), output[outputKeySentAt])
	r.InDelta(1500.0, output[outputKeyLatencyMs], 1.0)
	r.InDelta(1500.0, metrics[outputKeyLatencyMs], 1.0)
}

func TestEnrichFromSMTPHeadersAbsent(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	email := jmap.Email{ReceivedAt: time.Now()}

	output := models.JSONMap{outputKeyMessage: "Email received"}
	metrics := models.JSONMap{}
	enrichFromSMTPHeaders(output, metrics, &email, "org-uid")

	// Byte-identical behavior: no new keys appear when the headers are
	// missing (backward compatibility with human/heartbeat-style uses).
	r.Len(output, 1)
	r.Empty(metrics)
}

func TestEnrichFromSMTPHeadersMalformedSentAt(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	email := jmap.Email{
		ReceivedAt: time.Now(),
		Headers: []jmap.EmailHeader{
			{Name: headerSourceCheckUID, Value: "sending-check-uid"},
			{Name: headerSentAt, Value: "not-a-timestamp"},
		},
	}

	output := models.JSONMap{}
	metrics := models.JSONMap{}
	enrichFromSMTPHeaders(output, metrics, &email, "org-uid")

	r.Empty(output, "an unparsable Sent-At must degrade to plain behavior, not a partial enrichment")
	r.Empty(metrics)
}

func TestEnrichFromSMTPHeadersNegativeLatencyDropped(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// sentAt AFTER receivedAt: a clock-skew artifact (worker clock vs mail
	// path clock), not a real "arrived before it was sent".
	receivedAt := time.Now()
	sentAt := receivedAt.Add(5 * time.Second)

	email := jmap.Email{
		ReceivedAt: receivedAt,
		Headers: []jmap.EmailHeader{
			{Name: headerSourceCheckUID, Value: "sending-check-uid"},
			{Name: headerSentAt, Value: sentAt.Format(time.RFC3339)},
		},
	}

	output := models.JSONMap{}
	metrics := models.JSONMap{}
	enrichFromSMTPHeaders(output, metrics, &email, "org-uid")

	// Attribution is kept even when timing is untrustworthy.
	r.Equal("sending-check-uid", output[outputKeySourceCheckUID])
	r.Contains(output, outputKeySentAt)
	r.NotContains(output, outputKeyLatencyMs, "a negative latency must be dropped, never recorded")
	r.NotContains(metrics, outputKeyLatencyMs)
}

func TestEnrichFromSMTPHeadersExcessiveLatencyDropped(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	receivedAt := time.Now()
	sentAt := receivedAt.Add(-48 * time.Hour)

	email := jmap.Email{
		ReceivedAt: receivedAt,
		Headers: []jmap.EmailHeader{
			{Name: headerSourceCheckUID, Value: "sending-check-uid"},
			{Name: headerSentAt, Value: sentAt.Format(time.RFC3339)},
		},
	}

	output := models.JSONMap{}
	metrics := models.JSONMap{}
	enrichFromSMTPHeaders(output, metrics, &email, "org-uid")

	r.Equal("sending-check-uid", output[outputKeySourceCheckUID])
	r.NotContains(output, outputKeyLatencyMs, "an implausibly large latency must be dropped, not charted as real")
}
