package checkmysql_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkmysql"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel/sshtunneltest"
)

// tunnelHost cannot resolve locally — `.invalid` is reserved by RFC 2606. The
// bastion recording it verbatim PROVES go-sql-driver routed its dial through the
// tunnel (via the process-global `solidping-tunnel` dial network, whose dial func
// pulls the dialer from the connection context) and skipped local resolution.
const (
	tunnelHost   = "mysql.private.invalid"
	tunnelTarget = "mysql.private.invalid:3306"
)

func TestExecuteThroughTunnelSkipsLocalResolution(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// No Forward registered: the bastion refuses the target; the verbatim
	// host:port still crossed the tunnel, which is what we assert.
	srv := sshtunneltest.Start(t)
	dialer := srv.Dialer(t)

	checker := &checkmysql.MySQLChecker{}
	config := &checkmysql.MySQLConfig{}
	r.NoError(config.FromMap(map[string]any{
		"host":    tunnelHost,
		"port":    float64(3306),
		"timeout": "2s",
	}))

	ctx := checkerdef.WithTunnelDialer(t.Context(), dialer)

	result, err := checker.Execute(ctx, config)
	r.NoError(err)

	r.Contains(srv.Requested(), tunnelTarget)
	r.NoError(dialer.TunnelFailure())
	r.NotEqual(checkerdef.StatusUp, result.Status)
}

// Untunneled, a `.invalid` host must still fail locally.
func TestExecuteWithoutTunnelStillResolvesLocally(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &checkmysql.MySQLChecker{}
	config := &checkmysql.MySQLConfig{}
	r.NoError(config.FromMap(map[string]any{
		"host":    tunnelHost,
		"port":    float64(3306),
		"timeout": "2s",
	}))

	result, err := checker.Execute(t.Context(), config)
	r.NoError(err)
	r.NotEqual(checkerdef.StatusUp, result.Status)
}
