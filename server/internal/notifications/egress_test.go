package notifications

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// loopbackJobCtx is newJobCtx carrying an enforcing egress guard, so a sender
// under test goes through the same policy production wiring gives it (spec
// 2026-09-25-20) instead of the "no policy" default every other sender test
// in this package uses.
func loopbackJobCtx() *jobdef.JobContext {
	return &jobdef.JobContext{
		Logger:   slog.Default(),
		Services: &services.Registry{EgressGuard: egress.New(false)},
	}
}

// TestWebhookSender_Send_RefusesLoopbackTarget is the spec's "sender test":
// a webhook pointing at a loopback test server under an enforcing egress
// policy fails delivery with a clear error and issues no request at all —
// the defensive ValidateSenderURL check inside Send runs before any HTTP call
// is attempted, so the test server's handler must never fire.
func TestWebhookSender_Send_RefusesLoopbackTarget(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var requestsReceived atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestsReceived.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	payload := testPayload(models.JSONMap{"url": srv.URL})

	sender := &WebhookSender{}
	err := sender.Send(context.Background(), loopbackJobCtx(), payload)

	r.Error(err)
	r.ErrorIs(err, ErrSenderURLInvalid)

	var denied *egress.DeniedError
	r.ErrorAs(err, &denied)

	r.Zero(requestsReceived.Load(), "the loopback server must never receive a request")
	r.Nil(payload.DeliveryDetails, "a pre-flight rejection produces no delivery artifacts")
}

// TestWebhookSender_Send_AllowsLoopbackWhenPrivateTargetsAllowed is the
// control: the same loopback target succeeds once the guard allows private
// targets (self-hosted/test-mode default), proving the refusal above is the
// policy, not a broken loopback dial.
func TestWebhookSender_Send_AllowsLoopbackWhenPrivateTargetsAllowed(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var requestsReceived atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestsReceived.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	payload := testPayload(models.JSONMap{"url": srv.URL})

	sender := &WebhookSender{}
	jctx := &jobdef.JobContext{
		Logger:   slog.Default(),
		Services: &services.Registry{EgressGuard: egress.New(true)},
	}

	err := sender.Send(context.Background(), jctx, payload)
	r.NoError(err)
	r.Equal(int32(1), requestsReceived.Load())
}

// TestEgressGuardFrom_NilSafety pins the fallback every sender relies on: a
// bare JobContext (no Services) or a nil JobContext both resolve to a nil
// guard, i.e. "no policy" — the construction every sender's other unit tests
// use.
func TestEgressGuardFrom_NilSafety(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Nil(egressGuardFrom(nil))
	r.Nil(egressGuardFrom(&jobdef.JobContext{}))
	r.Nil(egressGuardFrom(newJobCtx()))

	guard := egress.New(false)
	r.Same(guard, egressGuardFrom(&jobdef.JobContext{Services: &services.Registry{EgressGuard: guard}}))
}
