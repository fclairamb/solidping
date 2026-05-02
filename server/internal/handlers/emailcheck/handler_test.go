package emailcheck

import (
	"testing"

	"github.com/stretchr/testify/require"

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
