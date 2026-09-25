package checkhttp_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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
