package agents_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// TestResultFrameDiagnosticsAbsent is the old-agent case: a result frame with
// no diagnostics must neither emit the key nor decode into a non-nil value.
func TestResultFrameDiagnosticsAbsent(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	frame := agents.ClientFrame{
		Type:   agents.MsgTypeResult,
		JobUID: "job-1",
		Status: 4,
		Output: map[string]any{"error": "boom"},
	}

	raw, err := json.Marshal(frame)
	r.NoError(err)
	// Positive control: the frame really did serialize its other fields.
	r.Contains(string(raw), `"jobUid":"job-1"`)
	r.NotContains(string(raw), "diagnostics")

	// And an old agent's frame (literally no key) decodes to nil, not to an
	// empty struct that downstream code would have to distinguish.
	var decoded agents.ClientFrame

	r.NoError(json.Unmarshal([]byte(`{"type":"result","jobUid":"job-1","status":4}`), &decoded))
	r.Nil(decoded.Diagnostics)
}

// TestResultFrameDiagnosticsRoundTrip is the new-agent case: every captured
// field survives the wire unchanged.
func TestResultFrameDiagnosticsRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	capturedAt := time.Date(2026, 8, 20, 10, 30, 0, 0, time.UTC)
	frame := agents.ClientFrame{
		Type:   agents.MsgTypeResult,
		JobUID: "job-1",
		Status: 4,
		Output: map[string]any{"error": "unexpected status code"},
		Diagnostics: &checkerdef.Diagnostics{
			FailureResponse: &checkerdef.FailureResponse{
				URL:           "https://acme.com/health",
				StatusLine:    "HTTP/2.0 503 Service Unavailable",
				StatusCode:    503,
				Headers:       map[string]string{"Server": "acme-lb", "Set-Cookie": "[redacted]"},
				Body:          "<html>acme is down</html>",
				Truncated:     true,
				ContentLength: 4096,
				ContentType:   "text/html; charset=utf-8",
				BodyBytes:     4096,
				BodySHA256:    strings.Repeat("ab", 32),
				CapturedAt:    capturedAt,
				RemoteAddr:    "203.0.113.10:443",
			},
		},
	}

	raw, err := json.Marshal(frame)
	r.NoError(err)
	r.Contains(string(raw), `"diagnostics"`)
	r.Contains(string(raw), `"failureResponse"`)

	var decoded agents.ClientFrame

	r.NoError(json.Unmarshal(raw, &decoded))
	r.NotNil(decoded.Diagnostics)
	r.NotNil(decoded.Diagnostics.FailureResponse)
	r.Equal(*frame.Diagnostics.FailureResponse, *decoded.Diagnostics.FailureResponse)
}

// TestDiagnosticsOmitsEmptyFields keeps the frame small for the common case: a
// capture with only the metadata a binary body allows must not carry empty
// body/header keys.
func TestDiagnosticsOmitsEmptyFields(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	diag := &checkerdef.Diagnostics{
		FailureResponse: &checkerdef.FailureResponse{
			StatusCode: 502,
			Binary:     true,
			BodyBytes:  17,
			BodySHA256: "deadbeef",
		},
	}

	raw, err := json.Marshal(diag)
	r.NoError(err)
	// Positive control.
	r.Contains(string(raw), `"binary":true`)
	r.NotContains(string(raw), `"body"`)
	r.NotContains(string(raw), `"headers"`)
	r.NotContains(string(raw), `"truncated"`)
	r.NotContains(string(raw), `"region"`)
	r.NotContains(string(raw), `"capturedAt"`)

	// An empty Diagnostics envelope serializes to `{}` rather than dragging a
	// null failureResponse across the wire.
	raw, err = json.Marshal(&checkerdef.Diagnostics{})
	r.NoError(err)
	r.JSONEq(`{}`, string(raw))
}
