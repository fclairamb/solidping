package incidentpublications_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/heartbeat"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentpublications"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// TestHeartbeatPathAutoPublishes is a WIRING test, not a policy test.
//
// The publication hook has to be installed on every incidents.Service that
// processes results, and there are several: the HTTP server's, the agent-worker
// one, the in-process check worker's, heartbeat and email checks. Each is
// constructed separately, and an instance built without the hook silently never
// auto-publishes — a gap nobody notices until an outage fails to reach the
// status page. The policy tests all drive one hand-built service and therefore
// cannot catch that.
//
// This drives the REAL heartbeat.NewService, so it fails if the hook is ever
// dropped from that constructor's call path.
func TestHeartbeatPathAutoPublishes(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	page := models.NewStatusPage(org.UID, "Acme status", "public")
	page.AutoPublish = true
	page.AutoPublishDelaySeconds = 0
	r.NoError(dbSvc.CreateStatusPage(ctx, page))

	section := models.NewStatusPageSection(page.UID, "Services", "services", 1)
	r.NoError(dbSvc.CreateStatusPageSection(ctx, section))

	token := "0123456789abcdef0123456789abcdef"
	check := models.NewCheck(org.UID, "nightly-backup", "heartbeat")
	check.Status = models.CheckStatusUp
	check.ConfirmationPeriodSeconds = 0
	check.RecoveryPeriodSeconds = 0
	check.Config = models.JSONMap{"token": token, "periodSeconds": 60}
	r.NoError(dbSvc.CreateCheck(ctx, check))

	publicName := "Nightly backup"
	resource := models.NewStatusPageResource(section.UID, check.UID, 1)
	resource.PublicName = &publicName
	r.NoError(dbSvc.CreateStatusPageResource(ctx, resource))

	// The publication service exactly as the server builds it, handed to the
	// heartbeat constructor the same way.
	pubs := incidentpublications.NewService(dbSvc, clock.Real{}, nil)
	pubs.SetJobsService(jobs)

	hbSvc := heartbeat.NewService(dbSvc, jobs, nil, pubs)

	// A failing heartbeat ping: this is a real inbound signal, not a synthetic
	// call into the incident service.
	r.NoError(hbSvc.ReceiveHeartbeat(
		ctx, org.Slug, *check.Slug, token, "down", "backup job failed", 0, "", "", "POST", nil))

	stored, err := dbSvc.ListIncidentPublications(ctx, &models.ListIncidentPublicationsFilter{
		OrganizationUID: org.UID,
		StatusPageUID:   page.UID,
		Limit:           10,
	})
	r.NoError(err)
	r.Len(stored, 1,
		"a heartbeat that opens an incident must auto-publish like any other result path")
	r.True(stored[0].AutoCreated)
	r.Contains(stored[0].PublicTitle, publicName)

	// The ping's own message is caller-supplied text on the internal result. It
	// must not have traveled into the public title.
	r.NotContains(stored[0].PublicTitle, "backup job failed")
}
