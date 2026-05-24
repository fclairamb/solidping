package slack

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests pin the transport-agnostic contract of the Dispatch* functions:
// the HTTP webhook handler and the Socket Mode supervisor both route through
// them, so the unknown-route fallbacks must behave consistently regardless
// of caller.

func TestDispatchCommand_UnknownCommandReturnsEphemeralReply(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	// svc is unused for the unknown-command path — it returns before any
	// service call. Passing nil keeps the test fully transport-agnostic.
	resp, err := DispatchCommand(ctx, nil, &Command{Command: "/totally-unknown"})

	r.NoError(err)
	r.NotNil(resp)
	r.Equal(ResponseTypeEphemeral, resp.ResponseType)
	r.Contains(resp.Text, "Unknown command: /totally-unknown")
}

func TestDispatchEvent_UnknownTypeIsNoop(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	// "unknown_event" is not in the dispatcher switch — it must return nil
	// without touching svc.
	err := DispatchEvent(ctx, nil, &Event{TeamID: "T1", Event: EventPayload{Type: "definitely_not_a_real_event"}})
	r.NoError(err)
}

func TestDispatchInteraction_ViewClosedIsNoop(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	// view_closed has a dedicated no-op branch that returns an empty
	// MessageResponse — confirms the dispatcher follows the same shape
	// as the HTTP handler did before the refactor.
	resp, err := DispatchInteraction(ctx, nil, &Interaction{
		Type: "view_closed",
		Team: Team{ID: "T1"},
	})
	r.NoError(err)
	r.NotNil(resp)
	r.Empty(resp.Text)
}

func TestDispatchInteraction_UnknownTypeReturnsEmpty(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	resp, err := DispatchInteraction(ctx, nil, &Interaction{
		Type: "totally_unhandled",
		Team: Team{ID: "T1"},
	})
	r.NoError(err)
	r.NotNil(resp)
	r.Empty(resp.Text)
}
