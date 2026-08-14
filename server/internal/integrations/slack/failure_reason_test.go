// Package slack_test exercises getFailureReason from outside the slack
// package (via GetFailureReasonForTest, export_test.go) so it can import
// internal/handlers/incidents to build a real incident. An in-package test
// file cannot do this: incidents imports internal/jobs/jobtypes, which
// imports this package (as slackclient, for escalation steps), so an
// in-package (slack) test file importing incidents would be a cycle.
package slack_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/integrations/slack"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// TestGetFailureReason_RealIncidentEndToEnd proves the twin getFailureReason
// in this package (interactions.go — used by Slack's interactive button
// handlers, identical logic to internal/notifications/slack.go's copy) is
// also alive: it must return the real cause pulled from incident.Details,
// not the "Check failed" fallback, for an incident opened by the real
// ProcessCheckResult path (not a hand-built models.Incident fixture). Spec
// 2026-08-13-11's Problem section names this exact function as one of the
// two dead readers this spec brings alive; before the fix this assertion
// failed here exactly as it did for the notifications package twin.
func TestGetFailureReason_RealIncidentEndToEnd(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clock.Real{}, nil)

	org := models.NewOrganization("slack-int-reason", "Slack Interactions Reason Test")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	check.ConfirmationPeriodSeconds = 0 // open on the very first failing result
	r.NoError(dbSvc.CreateCheck(ctx, check))

	status := int(models.ResultStatusDown)
	result := &models.Result{
		UID:             "result-1",
		OrganizationUID: org.UID,
		CheckUID:        check.UID,
		PeriodType:      models.PeriodTypeRaw,
		PeriodStart:     time.Now(),
		Status:          &status,
		Output: models.JSONMap{
			checkerdef.OutputKeyError: "DNS resolution failed: NXDOMAIN",
		},
	}

	// The real production entry point — same call the check executor makes
	// after persisting a raw result.
	r.NoError(svc.ProcessCheckResult(ctx, check, result))

	incident, err := dbSvc.FindActiveIncidentByCheckUID(ctx, check.UID)
	r.NoError(err)
	r.NotNil(incident, "ProcessCheckResult must have opened an incident")

	reason := slack.GetFailureReasonForTest(incident)
	r.Equal("DNS resolution failed: NXDOMAIN", reason)
	r.NotEqual("Check failed", reason,
		"getFailureReason must surface the real cause, not the dead-path fallback")
}
