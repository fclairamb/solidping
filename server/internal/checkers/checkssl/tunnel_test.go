package checkssl_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkssl"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel/sshtunneltest"
)

// tunnelHost cannot resolve locally — `.invalid` is reserved by RFC 2606 and
// never resolves anywhere. Only the fake bastion's forwarding table knows what
// it means, so reaching the target's TLS handshake at all PROVES checkssl
// skipped its local resolveHost: had it resolved locally first, the check would
// have errored before dialing.
const (
	tunnelHost   = "ssl.private.invalid"
	tunnelTarget = "ssl.private.invalid:8443"
)

// TestExecuteThroughTunnelSkipsLocalResolution proves the TCP dial is routed
// through the bastion and no local lookup happens. The httptest server presents
// a self-signed cert the system roots do not trust, so the *verification* fails
// (StatusDown) — but the handshake being attempted at all, plus the verbatim
// forward the bastion recorded, is what proves the tunnel dial path works.
func TestExecuteThroughTunnelSkipsLocalResolution(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	srv := sshtunneltest.Start(t)
	srv.Forward(tunnelTarget, backend.Listener.Addr().String())
	dialer := srv.Dialer(t)

	checker := &checkssl.SSLChecker{}
	config := &checkssl.SSLConfig{}
	r.NoError(config.FromMap(map[string]any{
		"host": tunnelHost,
		"port": float64(8443),
	}))

	ctx := checkerdef.WithTunnelDialer(t.Context(), dialer)

	result, err := checker.Execute(ctx, config)
	r.NoError(err)

	// The bastion forwarded the hostname verbatim — nothing local resolved it.
	r.Equal([]string{tunnelTarget}, srv.Requested())
	// The tunnel itself worked; the failure is the untrusted target cert.
	r.NoError(dialer.TunnelFailure())
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Contains(result.Output[checkerdef.OutputKeyError], "TLS handshake failed")
}

// Untunneled, a `.invalid` host must still fail on local resolution — proving
// the tunnel branch is what skips the lookup, not a blanket removal.
func TestExecuteWithoutTunnelStillResolvesLocally(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &checkssl.SSLChecker{}
	config := &checkssl.SSLConfig{}
	r.NoError(config.FromMap(map[string]any{"host": tunnelHost, "port": float64(8443)}))

	result, err := checker.Execute(t.Context(), config)
	r.NoError(err)
	r.Equal(checkerdef.StatusError, result.Status)
	r.Contains(result.Output[checkerdef.OutputKeyError], "failed to resolve hostname")
}
