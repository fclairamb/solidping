package emailcheck

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

// configuredCheckTimeout is deliberately unlike incidents.DefaultCheckTimeoutFallback
// so an assertion against it cannot pass by accident on a handler that never
// got the operator's value plumbed in.
const configuredCheckTimeout = 42 * time.Second

// TestNewHandlerWiresConfiguredCheckTimeout pins the email-check ingest path
// to the operator-configured `scheduling.check_timeout_ms`.
//
// An inbound mail opens and resolves incidents through the same rollup
// confirmation-hold gate as a probe result, whose per-ancestor hold cap ends
// in `TimeoutOrDefault(defaultCheckTimeout)`. Before this test the constructor
// silently kept incidents.DefaultCheckTimeoutFallback, so a deployment that
// tuned the ceiling got a different hold cap here than in the worker for the
// same check.
func TestNewHandlerWiresConfiguredCheckTimeout(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Positive control: without it the assertion below would still pass on the
	// unwired constructor that just returns the fallback.
	r.NotEqual(incidents.DefaultCheckTimeoutFallback, configuredCheckTimeout,
		"the configured value must differ from the fallback for this test to prove anything")

	h := NewHandler(nil, nil, nil, nil, nil, configuredCheckTimeout)
	r.Equal(configuredCheckTimeout, h.incidentSvc.DefaultCheckTimeout())
}

// TestNewHandlerNonPositiveCheckTimeoutKeepsFallback documents the other half
// of the contract: nothing configured (0, or a negative value from a
// malformed parameter) keeps the documented shipped default rather than a
// zero-length hold.
func TestNewHandlerNonPositiveCheckTimeoutKeepsFallback(t *testing.T) {
	t.Parallel()

	for name, timeout := range map[string]time.Duration{
		"zero":     0,
		"negative": -time.Second,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := NewHandler(nil, nil, nil, nil, nil, timeout)
			require.Equal(t, incidents.DefaultCheckTimeoutFallback,
				h.incidentSvc.DefaultCheckTimeout())
		})
	}
}
