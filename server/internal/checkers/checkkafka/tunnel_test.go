package checkkafka_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkkafka"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel/sshtunneltest"
)

// tunnelBroker cannot resolve locally — `.invalid` is reserved by RFC 2606. The
// bastion recording it verbatim PROVES sarama routed its broker dial through the
// tunnel and skipped local resolution.
const tunnelBroker = "kafka.private.invalid:9092"

func TestExecuteThroughTunnelSkipsLocalResolution(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// No Forward registered: the bastion refuses the broker; the verbatim
	// host:port still crossed the tunnel, which is what we assert.
	srv := sshtunneltest.Start(t)
	dialer := srv.Dialer(t)

	checker := &checkkafka.KafkaChecker{}
	config := &checkkafka.KafkaConfig{}
	r.NoError(config.FromMap(map[string]any{
		"brokers": []any{tunnelBroker},
		"timeout": "2s",
	}))

	ctx := checkerdef.WithTunnelDialer(t.Context(), dialer)

	result, err := checker.Execute(ctx, config)
	r.NoError(err)

	r.Contains(srv.Requested(), tunnelBroker)
	r.NoError(dialer.TunnelFailure())
	r.NotEqual(checkerdef.StatusUp, result.Status)
}

// Untunneled, a `.invalid` broker must still fail locally.
func TestExecuteWithoutTunnelStillResolvesLocally(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &checkkafka.KafkaChecker{}
	config := &checkkafka.KafkaConfig{}
	r.NoError(config.FromMap(map[string]any{
		"brokers": []any{tunnelBroker},
		"timeout": "2s",
	}))

	result, err := checker.Execute(t.Context(), config)
	r.NoError(err)
	r.NotEqual(checkerdef.StatusUp, result.Status)
}
