package heartbeat

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

// configuredCheckTimeout is deliberately unlike incidents.DefaultCheckTimeoutFallback
// so an assertion against it cannot pass by accident on a service that never
// got the operator's value plumbed in.
const configuredCheckTimeout = 42 * time.Second

// TestNewServiceWiresConfiguredCheckTimeout pins the heartbeat ingest path to
// the operator-configured `scheduling.check_timeout_ms`.
//
// A heartbeat ping runs the same rollup confirmation-hold gate as a probe
// result, and that gate's per-ancestor hold cap ends in
// `TimeoutOrDefault(defaultCheckTimeout)`. Before this test the constructor
// silently kept incidents.DefaultCheckTimeoutFallback, so an operator who
// raised or lowered the ceiling got a hold cap that depended on which ingest
// path happened to process the result.
func TestNewServiceWiresConfiguredCheckTimeout(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Positive control: without it the assertion below would still pass on the
	// unwired constructor that just returns the fallback.
	r.NotEqual(incidents.DefaultCheckTimeoutFallback, configuredCheckTimeout,
		"the configured value must differ from the fallback for this test to prove anything")

	svc := NewService(nil, nil, nil, nil, configuredCheckTimeout)
	r.Equal(configuredCheckTimeout, svc.incidentSvc.DefaultCheckTimeout())
}

// TestNewServiceNonPositiveCheckTimeoutKeepsFallback documents the other half
// of the contract: a caller with nothing configured (0, or a negative value
// from a malformed parameter) lands on the documented shipped default rather
// than a zero-length hold that would resolve every child immediately.
func TestNewServiceNonPositiveCheckTimeoutKeepsFallback(t *testing.T) {
	t.Parallel()

	for name, timeout := range map[string]time.Duration{
		"zero":     0,
		"negative": -time.Second,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			svc := NewService(nil, nil, nil, nil, timeout)
			require.Equal(t, incidents.DefaultCheckTimeoutFallback,
				svc.incidentSvc.DefaultCheckTimeout())
		})
	}
}
