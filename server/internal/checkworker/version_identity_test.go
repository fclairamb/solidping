package checkworker

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/version"
)

// TestRegisterWorkerReportsVersion proves the in-cluster worker's registration
// row carries this process's own build version (spec 2026-08-19-07). Heartbeats
// do not re-send it: it is fixed for the life of the process.
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

// errHeartbeatUnavailable is the failure the capture backend injects.
var errHeartbeatUnavailable = errors.New("database unavailable")

// captureHeartbeatBackend records the arguments updateHeartbeat passes to
// Heartbeat, and fails the next call when failNext is set. Only Heartbeat is
// exercised, mirroring captureRegisterBackend's "embed the interface, override
// one method" shape in identity_test.go.
type captureHeartbeatBackend struct {
	backend.WorkerBackend

	gotCaps    []string
	gotVersion string
	calls      int
	failNext   bool
}

func (b *captureHeartbeatBackend) Heartbeat(
	_ context.Context, _ string, caps []string, ver string,
) error {
	b.gotCaps, b.gotVersion = caps, ver
	b.calls++

	if b.failNext {
		b.failNext = false

		return errHeartbeatUnavailable
	}

	return nil
}

// TestUpdateHeartbeatWritesOnlyChangedCapabilities proves a beat never sends
// the version (registration wrote it, and it is fixed for the life of the
// process), and sends the capability set only when it differs from the one
// last stored. A failed beat is retried on the next one.
func TestUpdateHeartbeatWritesOnlyChangedCapabilities(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &config.Config{}
	fake := &captureHeartbeatBackend{}
	worker := newCheckWorker(cfg, fake)
	worker.setWorker(&models.Worker{UID: "w1"})

	// What registration stored differs from what the host reports now (the
	// egress probe answered after startup, say): the beat writes the new set.
	worker.writtenCaps = []string{"stale"}
	fake.failNext = true
	worker.updateHeartbeat(context.Background())
	r.NotNil(fake.gotCaps)
	r.Empty(fake.gotVersion)
	current := fake.gotCaps

	// That write failed, so the next beat sends the set again.
	worker.updateHeartbeat(context.Background())
	r.Equal(current, fake.gotCaps)

	// Stored: from now on only liveness.
	worker.updateHeartbeat(context.Background())
	r.Nil(fake.gotCaps)
	r.Empty(fake.gotVersion)
	r.Equal(3, fake.calls)
}
