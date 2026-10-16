package slack

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// TestCheckCommand_NonSlackResponseURLAnswersSynchronously: a response_url
// outside hooks.slack.com is treated like a missing one. The reply comes back
// in the HTTP response and nothing is sent to the attacker's URL.
func TestCheckCommand_NonSlackResponseURLAnswersSynchronously(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	h, svc, org := solidpingSetup(t)

	transport := &recordingTransport{}
	h.responseURLClient = newResponseURLClient(transport)

	cmd := solidpingCmd("check https://example.com")
	cmd.ResponseURL = "https://attacker.example/x"

	resp, err := h.handleSolidpingCommand(t.Context(), cmd)
	r.NoError(err)
	r.NotNil(resp)
	r.Equal(ResponseTypeInChannel, resp.ResponseType)
	r.Contains(resp.Text, "Check created")

	// The synchronous path has finished by the time the handler returns, so
	// the check exists now (positive control) and no goroutine is left to
	// post anywhere.
	listed, err := svc.checksService.ListChecks(t.Context(), org.Slug, checks.ListChecksOptions{})
	r.NoError(err)
	r.Len(listed.Data, 1)

	r.Empty(transport.seen(), "no request may reach a non-Slack response_url")
}
