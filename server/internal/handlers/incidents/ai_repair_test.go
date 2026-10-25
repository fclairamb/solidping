package incidents_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/aichecks"
	"github.com/fclairamb/solidping/server/internal/checkers/checkjs"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// TestDriftQueuesOneAIRepairJob: only a drift-class failure of an
// AI-authored js check queues the ai_repair job, and a run of them collapses
// into one pending job (spec 2026-10-03-07).
func TestDriftQueuesOneAIRepairJob(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	// Process-wide switch: no other test here has an AI-authored check.
	aichecks.SetEnabled(true)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clock.Real{}, nil)

	org := models.NewOrganization("ai-repair-test", "AI Repair Test")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	newCheck := func(slug string, config models.JSONMap) *models.Check {
		check := models.NewCheck(org.UID, slug, "js")
		check.Config = config
		r.NoError(dbSvc.CreateCheck(ctx, check))

		return check
	}

	aiCheck := newCheck("ai-check", models.JSONMap{
		"script": `return {status:"up"};`,
		"ai":     map[string]any{"prompt": "p", "contract": []any{"a"}},
	})
	plainCheck := newCheck("plain-check", models.JSONMap{"script": `return {status:"up"};`})

	submit := func(check *models.Check, status models.ResultStatus, output models.JSONMap) {
		result := models.NewResult(org.UID, check.UID, status, 0)
		result.Output = output
		r.NoError(dbSvc.CreateResult(ctx, result))
		r.NoError(svc.ProcessCheckResult(ctx, check, result))
	}

	repairJobs := func() int {
		list, listErr := jobs.ListJobs(ctx, org.UID, jobsvc.ListJobsOptions{Type: string(jobdef.JobTypeAIRepair)})
		r.NoError(listErr)

		return len(list)
	}

	// Not drift: nothing queued.
	submit(aiCheck, models.ResultStatusDown, models.JSONMap{"failure": "assertion"})
	submit(aiCheck, models.ResultStatusTimeout, models.JSONMap{"error": "script timed out after 30s"})
	submit(aiCheck, models.ResultStatusError, models.JSONMap{"error": "script error: dial tcp: connection refused"})
	// Drift on a check that is not AI-authored: nothing queued.
	submit(plainCheck, models.ResultStatusError, models.JSONMap{"error": "script error: TypeError"})
	r.Equal(0, repairJobs())

	// Drift, twice: one pending job.
	submit(aiCheck, models.ResultStatusDown, models.JSONMap{"failure": "drift"})
	submit(aiCheck, models.ResultStatusError, models.JSONMap{"error": "script error: TypeError"})
	r.Equal(1, repairJobs())
}

// TestRepairHostRefusalRevokesAutoRepair: a run of a repair-applied script
// refused by its repair grant queues no repair, flips the check back to
// `repair: propose` and records one check.ai_repair_revoked event (spec
// 2026-10-24-01).
func TestRepairHostRefusalRevokesAutoRepair(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	aichecks.SetEnabled(true)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clock.Real{}, nil)

	org := models.NewOrganization("ai-revoke-test", "AI Revoke Test")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "ai-auto", "js")
	check.Config = models.JSONMap{
		"script": `return {status:"up"};`,
		"ai":     map[string]any{"prompt": "p", "contract": []any{"a"}, "repair": "auto"},
	}
	r.NoError(dbSvc.CreateCheck(ctx, check))

	refused := models.JSONMap{
		"error":                        "the script reached a host its AI repair was not granted: [a host built at runtime]",
		checkjs.OutputKeyRepairRefused: true,
	}

	revocations := func() int {
		checkUID := check.UID
		events, listErr := dbSvc.ListEvents(ctx, &models.ListEventsFilter{
			OrganizationUID: org.UID, CheckUID: &checkUID,
			EventTypes: []models.EventType{models.EventTypeCheckAIRepairRevoked},
		})
		r.NoError(listErr)

		return len(events)
	}

	for range 2 {
		result := models.NewResult(org.UID, check.UID, models.ResultStatusError, 0)
		result.Output = refused
		r.NoError(dbSvc.CreateResult(ctx, result))
		r.NoError(svc.ProcessCheckResult(ctx, check, result))
	}

	stored, err := dbSvc.GetCheck(ctx, org.UID, check.UID)
	r.NoError(err)
	block, ok := stored.Config["ai"].(map[string]any)
	r.True(ok)
	r.Equal("propose", block["repair"])
	r.Equal(1, revocations(), "one flip, one event")

	list, err := jobs.ListJobs(ctx, org.UID, jobsvc.ListJobsOptions{Type: string(jobdef.JobTypeAIRepair)})
	r.NoError(err)
	r.Empty(list, "a refusal queues no repair")
}
