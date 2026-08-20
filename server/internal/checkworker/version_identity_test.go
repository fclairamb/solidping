package checkworker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/version"
)

// TestRegisterWorkerReportsVersion proves the in-cluster worker's registration
// row carries this process's own build version (spec 2026-08-19-07) — the
// same identity every claim/heartbeat re-reports.
func TestRegisterWorkerReportsVersion(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &config.Config{}
	cfg.Server.CheckWorker.Region = "eu"

	fake := &captureRegisterBackend{}
	worker := newCheckWorker(cfg, fake)

	r.NoError(worker.registerWorker(context.Background()))
	r.NotNil(fake.registered)
	r.NotNil(fake.registered.Version)
	r.Equal(version.Get().Version, *fake.registered.Version)
}

// captureHeartbeatBackend records the arguments updateHeartbeat passes to
// Heartbeat. Only Heartbeat is exercised, mirroring captureRegisterBackend's
// "embed the interface, override one method" shape in identity_test.go.
type captureHeartbeatBackend struct {
	backend.WorkerBackend

	gotVersion string
	calls      int
}

func (b *captureHeartbeatBackend) Heartbeat(
	_ context.Context, _ string, _ []string, ver string,
) error {
	b.gotVersion = ver
	b.calls++

	return nil
}

// TestUpdateHeartbeatReportsVersion proves updateHeartbeat re-sends this
// process's build version on every beat — cheap, since version.Get() is a
// package-level read, not a probe like the egress families.
func TestUpdateHeartbeatReportsVersion(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &config.Config{}
	fake := &captureHeartbeatBackend{}
	worker := newCheckWorker(cfg, fake)
	worker.setWorker(&models.Worker{UID: "w1"})

	worker.updateHeartbeat(context.Background())

	r.Equal(1, fake.calls)
	r.Equal(version.Get().Version, fake.gotVersion)
}
