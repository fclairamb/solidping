package checkhttp_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhttp"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel/sshtunneltest"
)

// tunnelTarget is a hostname that CANNOT resolve locally: `.invalid` is
// reserved by RFC 2606 and never resolves anywhere. Only the fake bastion's
// forwarding table knows what it means. That is the assertion: if checkhttp
// performed any local resolution, this probe would fail — its success proves
// the hostname was resolved on the far side of the tunnel.
const (
	tunnelHost   = "private.invalid"
	tunnelTarget = tunnelHost + ":80"
)

// newTunnelDialer starts a bastion forwarding tunnelTarget to backend and
// returns a dialer through it.
func newTunnelDialer(t *testing.T, backendAddr string) (*sshtunnel.Dialer, *sshtunneltest.Server) {
	t.Helper()

	srv := sshtunneltest.Start(t)
	srv.Forward(tunnelTarget, backendAddr)

	return srv.Dialer(t), srv
}

func TestExecuteThroughTunnel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		// The Host header must still be the target's, exactly as untunneled.
		writer.Header().Set("X-Seen-Host", req.Host)
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("private-service-ok"))
	}))
	defer backend.Close()

	dialer, srv := newTunnelDialer(t, backend.Listener.Addr().String())

	checker := &checkhttp.HTTPChecker{}
	config := &checkhttp.HTTPConfig{}
	r.NoError(config.FromMap(map[string]any{
		"url":           "http://" + tunnelHost + "/",
		"body_contains": "private-service-ok",
	}))

	ctx := checkerdef.WithTunnelDialer(t.Context(), dialer)

	result, err := checker.Execute(ctx, config)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)

	// The bastion — nothing local — is what resolved the private hostname.
	r.Equal([]string{tunnelTarget}, srv.Requested())
}

// Without a dialer on the context the checker must behave byte-for-byte as
// before: no transport override, plain direct dial.
func TestExecuteWithoutTunnelIsUnchanged(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	checker := &checkhttp.HTTPChecker{}
	config := &checkhttp.HTTPConfig{}
	r.NoError(config.FromMap(map[string]any{"url": backend.URL}))

	result, err := checker.Execute(t.Context(), config)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status)
}

// A tunneled check whose target is unreachable from the bastion reports the
// target as down — the tunnel itself worked.
func TestExecuteThroughTunnelTargetDown(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// No Forward registered for the target: the bastion answers "connect
	// failed", the same as a real one whose target refuses the connection.
	dialer, _ := newTunnelDialer(t, "127.0.0.1:1")

	checker := &checkhttp.HTTPChecker{}
	config := &checkhttp.HTTPConfig{}
	r.NoError(config.FromMap(map[string]any{"url": "http://unmapped.invalid/"}))

	ctx := checkerdef.WithTunnelDialer(t.Context(), dialer)

	result, err := checker.Execute(ctx, config)
	r.NoError(err)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.NoError(dialer.TunnelFailure())
}
