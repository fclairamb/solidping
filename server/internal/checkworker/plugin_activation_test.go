package checkworker

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checksleep"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// claimCaptureBackend records the plugin types advertised on each claim.
type claimCaptureBackend struct {
	*claimFailBackend
	advertised chan []string
}

func (b *claimCaptureBackend) ClaimJobs(
	ctx context.Context, _ string, _ *string, _, _, _ int, _ time.Duration,
) ([]*models.CheckJob, time.Duration, error) {
	b.advertised <- checkjobsvc.PluginTypesFromContext(ctx)

	return nil, 0, nil
}

func registerTestPlugin(t *testing.T, checkType string, labels []string) {
	t.Helper()

	meta := &checkerdef.CheckTypeMeta{
		Type:   checkerdef.CheckType(checkType),
		Labels: labels,
		Plugin: &checkerdef.PluginRef{Name: "test", Version: "1.0.0"},
	}
	require.NoError(t, registry.RegisterPlugin(meta,
		func() checkerdef.Checker { return &checksleep.SleepChecker{} },
		func() checkerdef.Config { return &checksleep.SleepConfig{} },
		func(*checkerdef.CheckSpec) error { return nil }))
	t.Cleanup(func() { registry.UnregisterPlugin(checkerdef.CheckType(checkType)) })
}

// A SaaS cloud worker does not advertise an unsafe plugin type at claim time,
// so it never claims its jobs; a self-hosted one does (spec 2026-10-17-05 §19).
func TestCloudWorkerDoesNotClaimUnsafePluginTypesInSaaS(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const (
		unsafeType = "x-claimtest-exec"
		safeType   = "x-claimtest-js"
	)

	registerTestPlugin(t, unsafeType, []string{"unsafe"})
	registerTestPlugin(t, safeType, []string{"safe"})

	claimed := func(mode string) []string {
		fake := &claimCaptureBackend{
			claimFailBackend: newClaimFailBackend(nil),
			advertised:       make(chan []string, 1),
		}
		worker := newCheckWorker(&config.Config{Deployment: config.DeploymentConfig{Mode: mode}}, fake)
		worker.setWorker(models.NewWorker("test-worker", "Test Worker"))
		worker.availableRunners.Store(1)

		_, err := worker.fetchAndDistributeJobs(context.Background(), slog.Default())
		r.NoError(err)

		return <-fake.advertised
	}

	saas := claimed(config.DeploymentModeSaaS)
	r.Contains(saas, safeType, "positive control: a safe plugin type is still claimed")
	r.NotContains(saas, unsafeType)
	r.NotContains(backend.Capabilities(context.Background(), saas), unsafeType)

	r.Contains(claimed(config.DeploymentModeSelfHosted), unsafeType)
}
