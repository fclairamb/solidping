package checkmongodb_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkmongodb"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel/sshtunneltest"
)

// tunnelHost cannot resolve locally — `.invalid` is reserved by RFC 2606. The
// bastion recording it verbatim PROVES the driver routed its dial through the
// tunnel and skipped local resolution.
const (
	tunnelHost   = "mongo.private.invalid"
	tunnelTarget = "mongo.private.invalid:27017"
)

func TestExecuteThroughTunnelSkipsLocalResolution(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// No Forward registered: the bastion refuses the target, so the dial fails
	// AT the bastion — but the verbatim host:port still crossed the tunnel.
	srv := sshtunneltest.Start(t)
	dialer := srv.Dialer(t)

	checker := &checkmongodb.MongoDBChecker{}
	config := &checkmongodb.MongoDBConfig{}
	r.NoError(config.FromMap(map[string]any{
		"host":    tunnelHost,
		"port":    float64(27017),
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

	checker := &checkmongodb.MongoDBChecker{}
	config := &checkmongodb.MongoDBConfig{}
	r.NoError(config.FromMap(map[string]any{
		"host":    tunnelHost,
		"port":    float64(27017),
		"timeout": "2s",
	}))

	result, err := checker.Execute(t.Context(), config)
	r.NoError(err)
	r.NotEqual(checkerdef.StatusUp, result.Status)
}
