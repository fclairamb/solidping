package jobtypes_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// faultSweepSkipped lists job types that reach the network, the filesystem or
// a long sleep regardless of the database, so a sweep would only be slow.
var faultSweepSkipped = map[jobdef.JobType]bool{ //nolint:gochecknoglobals // test table
	jobdef.JobTypeSleep:                     true,
	jobdef.JobTypeEmail:                     true,
	jobdef.JobTypeWebhook:                   true,
	jobdef.JobTypeAIRepair:                  true,
	jobdef.JobTypeNetworkDiscovery:          true,
	jobdef.JobTypeNetworkDiscoveryPlan:      true,
	jobdef.JobTypeNetworkDiscoveryAgentPlan: true,
	jobdef.JobTypeFreeboxLanDiscovery:       true,
	jobdef.JobTypeContainerDiscovery:        true,
	jobdef.JobTypeKubernetesDiscovery:       true,
	jobdef.JobTypeCustomDomainVerify:        true,
	jobdef.JobTypePlatformWatchdog:          true,
}

// TestJobsSurviveAFailingQuery runs every database-only job once per query it
// issues, failing a different one each time: a failed store must come back as
// an error (or be tolerated), never as a panic escaping the runner.
func TestJobsSurviveAFailingQuery(t *testing.T) {
	t.Parallel()

	for _, jobType := range jobtypes.RegisteredJobTypes() {
		if faultSweepSkipped[jobType] {
			continue
		}

		t.Run(string(jobType), func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()

			dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
			require.NoError(t, err)
			require.NoError(t, dbSvc.Initialize(ctx))
			t.Cleanup(func() { _ = dbSvc.Close() })

			org := models.NewOrganization("acme", "Acme")
			require.NoError(t, dbSvc.CreateOrganization(ctx, org))

			check := models.NewCheck(org.UID, "web", "http")
			check.Config = map[string]any{"url": "https://acme.com"}
			require.NoError(t, dbSvc.CreateCheck(ctx, check))

			hook := testsupport.InstallFaults(dbSvc.DB())
			jobSvc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

			def, ok := jobtypes.GetJobDefinition(jobType)
			require.True(t, ok)

			runner, err := def.CreateJobRun(json.RawMessage(`{}`))
			if err != nil {
				return
			}

			jctx := &jobdef.JobContext{
				OrganizationUID: &org.UID,
				Job:             &models.Job{UID: "job", OrganizationUID: &org.UID, Type: string(jobType)},
				Config:          json.RawMessage(`{}`),
				Services:        &services.Registry{Jobs: jobSvc},
				DB:              dbSvc.DB(),
				DBService:       dbSvc,
				AppConfig:       &config.Config{},
				Logger:          slog.Default(),
			}

			hook.Sweep(t, func(arm func()) {
				runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()

				defer func() { _ = recover() }() // nil services are outside this test's scope

				arm()
				_ = runner.Run(runCtx, jctx)
			})
		})
	}
}
