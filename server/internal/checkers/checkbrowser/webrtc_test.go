package checkbrowser

import (
	"context"
	"encoding/json"
	"net"
	"sync"
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

// popupFake is a fake browser whose session lives in the browser context
// "ctx-1" and that records every Target.closeTarget.
func popupFake(t *testing.T) (*fakeBrowser, func() []string) {
	t.Helper()

	fake := newFakeBrowser(t)
	fake.on("Target.createBrowserContext", func(json.RawMessage) (any, *fakeCDPError) {
		return map[string]any{"browserContextId": "ctx-1"}, nil
	})

	var (
		mu     sync.Mutex
		closed []string
	)

	fake.on("Target.closeTarget", func(params json.RawMessage) (any, *fakeCDPError) {
		var in struct {
			TargetID string `json:"targetId"`
		}

		_ = json.Unmarshal(params, &in)

		mu.Lock()
		closed = append(closed, in.TargetID)
		mu.Unlock()

		return map[string]any{"success": true}, nil
	})

	return fake, func() []string {
		mu.Lock()
		defer mu.Unlock()

		return append([]string(nil), closed...)
	}
}

// targetCreated emits Target.targetCreated for one target.
func targetCreated(fake *fakeBrowser, id, kind, subtype, browserContext string) {
	info := map[string]any{
		"targetId": id, "type": kind, "title": "", "url": "about:blank",
		"attached": false, "canAccessOpener": false, "browserContextId": browserContext,
	}
	if subtype != "" {
		info["subtype"] = subtype
	}

	fake.emit("Target.targetCreated", map[string]any{"targetInfo": info})
}

// Spec 2026-11-22-03: under an enforcing policy, a page target that a page
// of the session opens in the session's browser context is closed at once
// and poisons the session. The session's own page, a page of another
// browser context (another session on a shared Chrome) and a non-page
// target are left alone, and a prerender is closed without failing the check.
//
//nolint:paralleltest // mutates the process-wide settings
func TestEnforcingSessionClosesPopups(t *testing.T) {
	t.Run("a popup is closed and poisons the session", func(t *testing.T) {
		r := require.New(t)
		fake, closed := popupFake(t)

		withSettings(t, Settings{CDPURL: fake.url()})

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		ctx = egress.WithGuard(ctx, egress.New(false))

		session, err := Open(ctx)
		r.NoError(err)

		defer session.Close()

		_, err = session.Evaluate(ctx, "1")
		r.NoError(err)

		targetCreated(fake, "popup-1", "page", "", "ctx-1")

		r.Eventually(func() bool { return len(closed()) > 0 }, 5*time.Second, 10*time.Millisecond)
		r.Equal([]string{"popup-1"}, closed())

		_, err = session.Evaluate(ctx, "1")
		r.ErrorIs(err, errPopupRefused)
		r.False(Infra(err), "a popup is the page's doing, not the worker's")
	})

	t.Run("foreign, own and non-page targets are ignored", func(t *testing.T) {
		r := require.New(t)
		fake, closed := popupFake(t)

		withSettings(t, Settings{CDPURL: fake.url()})

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		ctx = egress.WithGuard(ctx, egress.New(false))

		session, err := Open(ctx)
		r.NoError(err)

		defer session.Close()

		targetCreated(fake, "foreign-1", "page", "", "ctx-other")
		targetCreated(fake, "fake-target", "page", "", "ctx-1")
		targetCreated(fake, "frame-1", "iframe", "", "ctx-1")
		targetCreated(fake, "worker-1", "worker", "", "ctx-1")
		// Events are handled in order: once the prerender is closed, every
		// event above has been seen.
		targetCreated(fake, "prerender-1", "page", prerenderSubtype, "ctx-1")

		r.Eventually(func() bool { return len(closed()) > 0 }, 5*time.Second, 10*time.Millisecond)
		r.Equal([]string{"prerender-1"}, closed())

		_, err = session.Evaluate(ctx, "1")
		r.NoError(err)
	})

	t.Run("not enforcing: popups are left alone", func(t *testing.T) {
		r := require.New(t)
		fake, closed := popupFake(t)

		withSettings(t, Settings{CDPURL: fake.url()})

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		ctx = egress.WithGuard(ctx, egress.New(true))

		session, err := Open(ctx)
		r.NoError(err)

		defer session.Close()

		targetCreated(fake, "popup-1", "page", "", "ctx-1")

		_, err = session.Evaluate(ctx, "1")
		r.NoError(err)
		r.Never(func() bool { return len(closed()) > 0 }, 300*time.Millisecond, 10*time.Millisecond)
	})
}
