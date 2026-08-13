// Package notifications_test exercises getFailureReason from outside the
// notifications package (via GetFailureReasonForTest, export_test.go) so it
// can import internal/handlers/incidents to build a real incident. An
// in-package test file cannot do this: incidents imports internal/jobs/jobtypes,
// which imports notifications, so an in-package (notifications) test file
// importing incidents would be a cycle.
package notifications_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifications"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// TestGetFailureReason_RealIncidentEndToEnd proves getFailureReason (the
// previously dead path: incident.Details["failure_reason"] / ["output"] were
// never populated, so every Slack notification fell back to "Check failed")
// now returns the real cause, end to end through the production incident
// open path — not a hand-built models.Incident{Details: ...} fixture. Before
// spec 2026-08-13-11 this assertion failed: getFailureReason returned the
// "Check failed" fallback for every incident, because nothing ever wrote to
// incident.Details.
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

	org := models.NewOrganization("slack-reason", "Slack Reason Test")
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
			checkerdef.OutputKeyError: "TLS handshake failed: certificate expired",
		},
	}

	// The real production entry point — same call the check executor makes
	// after persisting a raw result.
	r.NoError(svc.ProcessCheckResult(ctx, check, result))

	incident, err := dbSvc.FindActiveIncidentByCheckUID(ctx, check.UID)
	r.NoError(err)
	r.NotNil(incident, "ProcessCheckResult must have opened an incident")

	reason := notifications.GetFailureReasonForTest(incident)
	r.Equal("TLS handshake failed: certificate expired", reason)
	r.NotEqual("Check failed", reason,
		"getFailureReason must surface the real cause, not the dead-path fallback")
}
