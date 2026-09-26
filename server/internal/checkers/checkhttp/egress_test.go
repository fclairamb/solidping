package checkhttp_test

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhttp"
	"github.com/fclairamb/solidping/server/internal/egress"
)

const internalSecret = "iam-credentials-secret"

func internalServer(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(internalSecret))
	}))
	t.Cleanup(server.Close)

	return server
}

func egressConfig(t *testing.T, url string) *checkhttp.HTTPConfig {
	t.Helper()

	config := &checkhttp.HTTPConfig{}
	require.NoError(t, config.FromMap(map[string]any{
		"url":                      url,
		"body_expect":              internalSecret,
		"capture_failure_response": true,
	}))

	return config
}

// The SSRF read primitive, closed: under an enforcing egress policy an HTTP
// check aimed at a loopback service fails with the policy's error, and neither
// the output nor the incident diagnostics carry a byte of the response.
func TestExecuteRefusesALoopbackTargetUnderAnEnforcingPolicy(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	server := internalServer(t)

	ctx := egress.WithGuard(t.Context(), egress.New(false))

	result, err := (&checkhttp.HTTPChecker{}).Execute(ctx, egressConfig(t, server.URL+"/latest/meta-data/"))
	r.NoError(err)
	r.Equal(checkerdef.StatusError, result.Status, result.Output)
	r.Contains(result.Output[checkerdef.OutputKeyError], "denied by egress policy")
	r.Contains(result.Output[checkerdef.OutputKeyError], egress.EnvAllowPrivate)
	r.NotContains(fmt.Sprint(result.Output), internalSecret)

	if result.Diagnostics != nil {
		r.Nil(result.Diagnostics.FailureResponse, "nothing was connected, nothing can be captured")
	}
}

// Positive control: the same check, same server, under a permissive policy,
// reaches the target (and its body assertion passes).
func TestExecuteReachesALoopbackTargetUnderAPermissivePolicy(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	server := internalServer(t)

	for name, guard := range map[string]*egress.Guard{"allow": egress.New(true), "no guard": nil} {
		ctx := egress.WithGuard(t.Context(), guard)

		result, err := (&checkhttp.HTTPChecker{}).Execute(ctx, egressConfig(t, server.URL))
		r.NoError(err)
		r.NotEqual(checkerdef.StatusError, result.Status, name)
		r.NotContains(fmt.Sprint(result.Output[checkerdef.OutputKeyError]), "egress", name)
	}
}

// ipv6LoopbackServer starts an httptest server on the IPv6 loopback address
// instead of the default IPv4 one. It stands in for a "second address" a
// redirect test needs — a second real IPv4 loopback (127.0.0.2) is not
// portably bindable (macOS refuses it), but ::1 is guaranteed everywhere.
func ipv6LoopbackServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	server := httptest.NewUnstartedServer(handler)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "[::1]:0")
	require.NoError(t, err)

	_ = server.Listener.Close()
	server.Listener = ln
	server.Start()
	t.Cleanup(server.Close)

	return server
}

// redirectingServer 302s every request to target.
func redirectingServer(t *testing.T, target string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, target, http.StatusFound)
	}))
	t.Cleanup(server.Close)

	return server
}

// The redirect version of the SSRF read primitive: hop 1 is a "public"
// server (stood up on the IPv6 loopback address and treated as public by
// this guard only, via egress.WithPublicOverride — see that option's doc),
// hop 2 is a genuinely internal loopback service. Every hop dials through the
// SAME transport, so the guard re-validates hop 2's address exactly as it
// validated hop 1's — the whole point of enforcing at the dial layer rather
// than on the configured URL string. Hop 2 must never even be dialed.
func TestExecuteRedirectHopTwoIsRecheckedAtDialTime(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var hop2Hit atomic.Bool

	hop2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hop2Hit.Store(true)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(internalSecret))
	}))
	t.Cleanup(hop2.Close)

	hop1 := ipv6LoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, hop2.URL, http.StatusFound)
	}))

	guard := egress.New(false, egress.WithPublicOverride(net.ParseIP("::1")))
	ctx := egress.WithGuard(t.Context(), guard)

	result, err := (&checkhttp.HTTPChecker{}).Execute(ctx, egressConfig(t, hop1.URL))
	r.NoError(err)
	r.Equal(checkerdef.StatusError, result.Status, result.Output)
	r.Contains(result.Output[checkerdef.OutputKeyError], "denied by egress policy")
	r.False(hop2Hit.Load(), "hop 2 must never be dialed: the guard refuses it before any connection is made")
	r.NotContains(fmt.Sprint(result.Output), internalSecret)

	if result.Diagnostics != nil {
		r.Nil(result.Diagnostics.FailureResponse, "nothing was connected, nothing can be captured")
	}
}

// redirect_host_policy: same-host refuses a cross-host hop on its own,
// independent of the egress guard — proven here with allow_private=true (the
// IP policy would let hop 2 through) so only the host policy can be the
// reason hop 2 is never reached.
func TestExecuteRedirectHostPolicySameHostRefusesEvenWithPrivateAllowed(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var hop2Hit atomic.Bool

	hop2 := ipv6LoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hop2Hit.Store(true)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(internalSecret))
	}))

	hop1 := redirectingServer(t, hop2.URL)

	config := &checkhttp.HTTPConfig{}
	r.NoError(config.FromMap(map[string]any{
		"url":                      hop1.URL,
		"redirectHostPolicy":       "same-host",
		"body_expect":              internalSecret,
		"capture_failure_response": true,
	}))

	ctx := egress.WithGuard(t.Context(), egress.New(true))

	result, err := (&checkhttp.HTTPChecker{}).Execute(ctx, config)
	r.NoError(err)
	r.Equal(checkerdef.StatusDown, result.Status, result.Output)
	r.Contains(result.Output[checkerdef.OutputKeyError], "redirect to different host refused")
	r.False(hop2Hit.Load(), "the refused hop must never be dialed")
	r.NotContains(fmt.Sprint(result.Output), internalSecret)

	if result.Diagnostics != nil {
		r.Nil(result.Diagnostics.FailureResponse, "nothing was connected, nothing can be captured")
	}
}

// A tunneled check reaches the target from the BASTION's network: the guard
// does not apply to the forwarded connection.
func TestExecuteThroughTunnelIsNotSubjectToTheGuard(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("private-service-ok"))
	}))
	t.Cleanup(backend.Close)

	dialer, _ := newTunnelDialer(t, backend.Listener.Addr().String())

	config := &checkhttp.HTTPConfig{}
	r.NoError(config.FromMap(map[string]any{"url": "http://" + tunnelHost + "/", "body_expect": "private-service-ok"}))

	ctx := checkerdef.WithTunnelDialer(egress.WithGuard(t.Context(), egress.New(false)), dialer)

	result, err := (&checkhttp.HTTPChecker{}).Execute(ctx, config)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
}
