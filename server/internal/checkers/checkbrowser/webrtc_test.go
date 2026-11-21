package checkbrowser

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/egress"
)

// Spec 2026-11-20-01: the Chrome an enforcing session starts carries the
// WebRTC IP handling policy that forbids UDP around the proxy; a
// non-enforcing one (no proxy) does not.
func TestEgressExecFlagsSetTheWebRTCPolicyOnlyWhenEnforcing(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	proxy, err := startEgressProxy(t.Context(), egress.New(false), func(*egress.DeniedError) {})
	r.NoError(err)
	t.Cleanup(proxy.Close)

	pin := &hostPin{host: "www.acme.com", ip: net.ParseIP("93.184.216.34")}

	enforcing := egressExecFlags(pin, proxy)
	r.Equal(webRTCIPHandlingPolicy, enforcing["force-webrtc-ip-handling-policy"])
	r.Equal("disable_non_proxied_udp", enforcing["webrtc-ip-handling-policy"])
	r.Equal(proxy.URL(), enforcing["proxy-server"])
	r.Equal(egressProxyBypassList, enforcing["proxy-bypass-list"])
	r.Equal(egressResolverRules(pin, proxy.host()), enforcing["host-resolver-rules"])

	pinnedOnly := egressExecFlags(pin, nil)
	r.Equal(map[string]any{"host-resolver-rules": pin.resolverRule()}, pinnedOnly)
	r.NotContains(pinnedOnly, "force-webrtc-ip-handling-policy")

	r.Empty(egressExecFlags(nil, nil))
}

// An enforcing session installs the script removing WebRTC on every new
// document; a non-enforcing one leaves the page alone.
//
//nolint:paralleltest // mutates the process-wide settings
func TestEnforcingSessionInstallsTheWebRTCScript(t *testing.T) {
	for _, tc := range []struct {
		name      string
		guard     *egress.Guard
		installed bool
	}{
		{name: "enforcing", guard: egress.New(false), installed: true},
		{name: "not enforcing", guard: egress.New(true), installed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)
			fake := newFakeBrowser(t)

			var source string

			fake.on("Page.addScriptToEvaluateOnNewDocument", func(params json.RawMessage) (any, *fakeCDPError) {
				var in struct {
					Source string `json:"source"`
				}

				_ = json.Unmarshal(params, &in)
				source = in.Source

				return map[string]any{"identifier": "webrtc-1"}, nil
			})

			withSettings(t, Settings{CDPURL: fake.url()})

			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()

			session, err := Open(egress.WithGuard(ctx, tc.guard))
			r.NoError(err)

			defer session.Close()

			if !tc.installed {
				r.Zero(fake.called("Page.addScriptToEvaluateOnNewDocument"))
				r.Empty(session.webRTCScript)

				return
			}

			r.Equal(1, fake.called("Page.addScriptToEvaluateOnNewDocument"))
			r.Equal(disableWebRTCScript, source)
			r.EqualValues("webrtc-1", session.webRTCScript)
		})
	}
}

// A failure to install the script fails the open closed, as an
// infrastructure error, rather than running the check with WebRTC on.
//
//nolint:paralleltest // mutates the process-wide settings
func TestWebRTCScriptFailureFailsTheOpen(t *testing.T) {
	r := require.New(t)
	fake := newFakeBrowser(t)
	fake.on("Page.addScriptToEvaluateOnNewDocument", func(json.RawMessage) (any, *fakeCDPError) {
		return nil, &fakeCDPError{Code: -32000, Message: "boom"}
	})

	withSettings(t, Settings{CDPURL: fake.url()})

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	_, err := Open(egress.WithGuard(ctx, egress.New(false)))
	r.Error(err)
	r.True(Infra(err))
	r.Contains(err.Error(), "failed to disable WebRTC")
}
