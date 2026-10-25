package jobtypes_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
)

func TestIncidentPublishJobRunAndScheduler(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	jctx, dbSvc, orgUID := newAgentGCTestContext(t)
	jctx.AppConfig = &config.Config{Server: config.ServerConfig{BaseURL: "https://status.acme.com"}}
	def := &jobtypes.IncidentPublishJobDefinition{}
	r.Equal(jobdef.JobTypeIncidentPublish, def.Type())

	_, err := def.CreateJobRun(json.RawMessage(`not json`))
	r.Error(err)
	_, err = def.CreateJobRun(json.RawMessage(`{"incidentUid":"i"}`))
	r.ErrorIs(err, jobtypes.ErrIncidentPublishConfig)

	// No organization anywhere: a config error.
	run, err := def.CreateJobRun(json.RawMessage(`{"incidentUid":"i","statusPageUid":"p"}`))
	r.NoError(err)
	r.ErrorIs(run.Run(ctx, &jobdef.JobContext{DBService: dbSvc}), jobtypes.ErrIncidentPublishConfig)

	// The organization comes from the job context; the incident is unknown, so
	// the publish is retried.
	jctx.OrganizationUID = &orgUID
	err = run.Run(ctx, jctx)
	r.Error(err)
	var retryable *jobdef.RetryableError
	r.ErrorAs(err, &retryable)

	// Same without a services registry.
	r.Error(run.Run(ctx, &jobdef.JobContext{DBService: dbSvc, OrganizationUID: &orgUID, Logger: jctx.Logger}))

	// Scheduler: nil is a no-op, a real queue gets a job.
	var nilScheduler *jobtypes.IncidentPublishScheduler
	r.NoError(nilScheduler.SchedulePublish(ctx, orgUID, "i", "p", time.Now()))
	r.NoError(jobtypes.NewIncidentPublishScheduler(nil).SchedulePublish(ctx, orgUID, "i", "p", time.Now()))

	scheduler := jobtypes.NewIncidentPublishScheduler(jctx.Services.Jobs)
	r.NoError(scheduler.SchedulePublish(ctx, orgUID, "inc-1", "page-1", time.Now().Add(time.Minute)))

	// A closed database makes the enqueue fail.
	r.NoError(dbSvc.Close())
	r.Error(scheduler.SchedulePublish(ctx, orgUID, "inc-2", "page-1", time.Now()))
}
