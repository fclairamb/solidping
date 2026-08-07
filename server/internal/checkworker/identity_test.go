package checkworker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// captureRegisterBackend records the worker row registerWorker builds. Only
// Register is exercised; the embedded interface satisfies the rest and would
// panic if anything else were called, which is exactly the assertion we want.
type captureRegisterBackend struct {
	backend.WorkerBackend

	registered *models.Worker
}

func (b *captureRegisterBackend) Register(
	_ context.Context, worker *models.Worker,
) (*models.Worker, error) {
	b.registered = worker

	return worker, nil
}

// TestRegisterWorkerIdentity proves the check worker's `workers` row takes its
// slug and name from SP_NODE_NAME when configured, and falls back to the
// lowercased/truncated hostname otherwise — the end of the chain the
// config-level tests cover from the other side.
func TestRegisterWorkerIdentity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		nodeName  string
		wantSlug  string
		wantName  string
		wantExact bool
	}{
		{
			name:      "override registers verbatim",
			nodeName:  "solidping-eu2",
			wantSlug:  "solidping-eu2",
			wantName:  "solidping-eu2",
			wantExact: true,
		},
		{
			// No override: the row must match whatever the shared resolver
			// derives from the host, byte-for-byte.
			name: "no override falls back to the hostname",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			cfg := &config.Config{}
			cfg.Node.Name = tc.nodeName
			cfg.Server.CheckWorker.Region = "eu"

			fake := &captureRegisterBackend{}
			worker := newCheckWorker(cfg, fake)

			r.NoError(worker.registerWorker(context.Background()))
			r.NotNil(fake.registered)

			wantSlug, wantName := tc.wantSlug, tc.wantName
			if !tc.wantExact {
				identity := cfg.WorkerIdentity()
				wantSlug, wantName = identity.Slug, identity.Name
			}

			r.Equal(wantSlug, fake.registered.Slug)
			r.Equal(wantName, fake.registered.Name)
			r.NotNil(fake.registered.Region)
			r.Equal("eu", *fake.registered.Region)

			// The registered identity is published for the rest of the loop.
			r.NotNil(worker.getWorker())
			r.Equal(wantSlug, worker.getWorker().Slug)
		})
	}
}
