package tracediag

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/egress"
)

// A local trace is an outbound probe from this worker: under an enforcing
// egress policy it is never sent towards a non-public address — that would map
// the very internal network the check was refused. A public address still is.
func TestDispatcherHonorsTheLocalEgressPolicy(t *testing.T) {
	t.Parallel()

	store := &recordingStore{}
	dispatcher, done := newLocalDispatcher(t, store, stubCapture)
	dispatcher.SetEgressGuard(egress.New(false))

	refused := testRequest()
	refused.Failure.Address = "169.254.169.254"

	dispatcher.RequestTrace(t.Context(), refused)

	select {
	case <-done:
		t.Fatal("traced a non-public address under an enforcing policy")
	case <-time.After(200 * time.Millisecond):
	}

	require.Empty(t, store.snapshot())

	// Positive control: the same dispatcher traces a public address.
	allowed := testRequest()
	allowed.IncidentUID = "incident-2"
	allowed.Failure.Address = "93.184.216.34"

	dispatcher.RequestTrace(t.Context(), allowed)
	waitFor(t, done)
	require.Len(t, store.snapshot(), 1)
}

// A permissive policy (self-hosted) traces private addresses as before.
func TestDispatcherTracesPrivateAddressesUnderAPermissivePolicy(t *testing.T) {
	t.Parallel()

	store := &recordingStore{}
	dispatcher, done := newLocalDispatcher(t, store, stubCapture)
	dispatcher.SetEgressGuard(egress.New(true))

	req := testRequest()
	req.Failure.Address = "10.0.0.7"

	dispatcher.RequestTrace(t.Context(), req)
	waitFor(t, done)
	require.Len(t, store.snapshot(), 1)
}
