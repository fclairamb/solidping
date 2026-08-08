package checkclickhouse_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel/sshtunneltest"
)

// tunnelHost cannot resolve locally — `.invalid` is reserved by RFC 2606. The
// bastion recording it verbatim PROVES clickhouse-go routed its dial through
// the tunnel (via Options.DialContext) and skipped local resolution.
const (
	tunnelHost   = "clickhouse.private.invalid"
	tunnelTarget = "clickhouse.private.invalid:9000"
)

func TestExecuteThroughTunnelSkipsLocalResolution(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// No Forward registered: the bastion refuses the target; the verbatim
	// host:port still crossed the tunnel, which is what we assert.
	srv := sshtunneltest.Start(t)
	dialer := srv.Dialer(t)

	ctx := checkerdef.WithTunnelDialer(t.Context(), dialer)

	result := execute(ctx, t, map[string]any{
		"host":    tunnelHost,
		"timeout": "5s",
	})

	r.Contains(srv.Requested(), tunnelTarget)
	r.NotEqual(checkerdef.StatusUp, result.Status)
	r.Equal(true, result.Output["tunneled"])
}

// Untunneled, a `.invalid` host must still fail locally.
func TestExecuteWithoutTunnelStillResolvesLocally(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := execute(t.Context(), t, map[string]any{
		"host":    tunnelHost,
		"timeout": "5s",
	})

	r.NotEqual(checkerdef.StatusUp, result.Status)
	r.NotContains(result.Output, "tunneled")
}

// The TLS transport must move the default port to 9440 on the wire, not just in
// the generated name.
func TestSecureDefaultsToTLSPortThroughTunnel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := sshtunneltest.Start(t)
	dialer := srv.Dialer(t)

	ctx := checkerdef.WithTunnelDialer(t.Context(), dialer)

	execute(ctx, t, map[string]any{
		"host":    tunnelHost,
		"secure":  true,
		"timeout": "5s",
	})

	r.Contains(srv.Requested(), tunnelHost+":9440")
}
