package slack

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBuildAppHomeView_OnDispatchEventHandler pins the invariant that the App
// Home view can be built from the transport-less *Handler that DispatchEvent
// constructs (events.go: `dispatcher := &Handler{svc: svc}`), which leaves cfg
// nil. Reading h.cfg here panics on every app_home_opened arriving through that
// path — including Socket Mode. Configuration must come from h.svc.cfg, which
// every transport sets.
func TestBuildAppHomeView_OnDispatchEventHandler(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, svc := setupSlackService(t)

	// Exactly how DispatchEvent builds it: svc only, cfg deliberately nil.
	handler := &Handler{svc: svc}

	var view *AppHomeView
	r.NotPanics(func() {
		view = handler.buildAppHomeView(t.Context(), "T012345")
	})
	r.NotNil(view)
	r.Equal("home", view.Type)

	encoded, err := json.Marshal(view.Blocks)
	r.NoError(err)
	rendered := string(encoded)

	// The dashboard button follows the deployment, not solidping.io: a
	// self-hosted instance must not send its users to the hosted service.
	r.Contains(rendered, "http://localhost:4000/dashboard")

	// Slack Marketplace guidelines require App Home to surface support
	// information and pricing.
	for _, want := range []string{pricingURL, supportURL, supportEmail} {
		r.Contains(rendered, want)
	}
}

// TestBuildAppHomeView_DoesNotAdvertiseSlashSolidping guards against the
// failure this area already has once: both manifests and the landing page
// advertise /solidping subcommands that DispatchCommand has no case for
// (commands.go handles only /check and /comment), so they answer
// "Unknown command: /solidping". App Home must not repeat that claim.
//
// Delete this test in the spec that adds the handler —
// specs/todos/2026-08-29-02-slack-slash-command-namespace.md.
func TestBuildAppHomeView_DoesNotAdvertiseSlashSolidping(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, svc := setupSlackService(t)

	handler := &Handler{svc: svc}
	encoded, err := json.Marshal(handler.buildAppHomeView(t.Context(), "T012345").Blocks)
	r.NoError(err)

	r.NotContains(string(encoded), "/solidping",
		"App Home advertises /solidping, which DispatchCommand does not handle")
}
