package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/i18n/recipient"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
)

var errReportJobsUnavailable = errors.New("jobs service unavailable")

// reportTestSendEnqueuer adapts the job queue to reportschedules.TestSendEnqueuer,
// so the handler package never depends on the jobs packages.
type reportTestSendEnqueuer struct {
	services *services.Registry
}

// EnqueueTestSend queues one report_test_send job and returns its uid.
func (e *reportTestSendEnqueuer) EnqueueTestSend(
	ctx context.Context, orgUID, scheduleUID, address string, hint recipient.Hint, now time.Time,
) (string, error) {
	if e.services == nil || e.services.Jobs == nil {
		return "", errReportJobsUnavailable
	}

	raw, err := json.Marshal(jobtypes.ReportTestSendJobConfig{
		OrgUID:      orgUID,
		ScheduleUID: scheduleUID,
		Address:     address,
		Lang:        hint.Locale,
		Timezone:    hint.Timezone,
		Now:         now,
	})
	if err != nil {
		return "", fmt.Errorf("marshal report test send: %w", err)
	}

	job, err := e.services.Jobs.CreateJob(ctx, orgUID, string(jobdef.JobTypeReportTestSend), raw, nil)
	if err != nil {
		return "", fmt.Errorf("enqueue report test send: %w", err)
	}

	return job.UID, nil
}
