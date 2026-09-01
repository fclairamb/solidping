package mcp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

// configuredCheckTimeoutMs is deliberately unlike the shipped 15000 that
// incidents.DefaultCheckTimeoutFallback mirrors, so an assertion against it
// cannot pass by accident on a handler that never read cfg.
const configuredCheckTimeoutMs = 42000

// TestNewHandlerWiresConfiguredCheckTimeout pins the MCP ingest path to the
// operator-configured `scheduling.check_timeout_ms`.
//
// MCP opens and resolves incidents through the same rollup confirmation-hold
// gate as a probe result, whose per-ancestor hold cap ends in
// `TimeoutOrDefault(defaultCheckTimeout)`. Before this test the constructor
// ignored the cfg it was already being handed and silently kept
// incidents.DefaultCheckTimeoutFallback.
func TestNewHandlerWiresConfiguredCheckTimeout(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &config.Config{}
	cfg.Server.Scheduling.CheckTimeoutMs = configuredCheckTimeoutMs

	want := configuredCheckTimeoutMs * time.Millisecond

	// Positive control: without it the assertion below would still pass on the
	// unwired constructor that just returns the fallback.
	r.NotEqual(incidents.DefaultCheckTimeoutFallback, want,
		"the configured value must differ from the fallback for this test to prove anything")

	h := NewHandler(nil, nil, nil, nil, nil, nil, nil, cfg)
	r.Equal(want, h.incidentsSvc.DefaultCheckTimeout())
}

// TestNewHandlerWithoutConfigKeepsFallback covers the nil-cfg construction
// path (tests, and any future caller with no app config to hand): it must
// land on the documented shipped default, not a zero-length hold.
func TestNewHandlerWithoutConfigKeepsFallback(t *testing.T) {
	t.Parallel()

	h := NewHandler(nil, nil, nil, nil, nil, nil, nil, nil)
	require.Equal(t, incidents.DefaultCheckTimeoutFallback,
		h.incidentsSvc.DefaultCheckTimeout())
}
