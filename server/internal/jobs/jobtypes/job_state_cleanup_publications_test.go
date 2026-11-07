package jobtypes

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentpublications"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// TestStateCleanupClosesEntriesOfDeletedChecks pins the periodic half of spec
// 2026-11-05-05: the state_cleanup sweep resolves a status-page entry whose
// check was deleted without the publication hook running (a crash between the
// soft delete and the notification, or a delete from before the hook existed).
func TestStateCleanupClosesEntriesOfDeletedChecks(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, dbSvc, org := attachmentGCSetup(t)

	page := models.NewStatusPage(org.UID, "Acme status", "public")
	page.AutoPublish = true
	page.AutoResolve = string(models.AutoResolveIfUntouched)
	r.NoError(dbSvc.CreateStatusPage(ctx, page))

	section := models.NewStatusPageSection(page.UID, "Services", "services", 1)
	r.NoError(dbSvc.CreateStatusPageSection(ctx, section))

	check := models.NewCheck(org.UID, "payments-api", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	publicName := "Payments API"
	resource := models.NewStatusPageResource(section.UID, check.UID, 1)
	resource.PublicName = &publicName
	r.NoError(dbSvc.CreateStatusPageResource(ctx, resource))

	incident := models.NewIncident(org.UID, check.UID, check.CreatedAt, "payments-api is down")
	r.NoError(dbSvc.CreateIncident(ctx, incident))
	incidentpublications.NewService(dbSvc, nil, nil).OnIncidentOpened(ctx, incident)

	listOpen := func() []*models.IncidentPublication {
		pubs, err := dbSvc.ListIncidentPublications(ctx, &models.ListIncidentPublicationsFilter{
			OrganizationUID: org.UID, ActiveOnly: true,
		})
		r.NoError(err)

		return pubs
	}

	r.Len(listOpen(), 1, "precondition: the incident is on the page")

	// The DB half of a delete only: the incident is resolved, the entry is not.
	r.NoError(dbSvc.DeleteCheck(ctx, check.UID))
	r.Len(listOpen(), 1)

	sweepOrphanedPublications(ctx, &jobdef.JobContext{DBService: dbSvc, Logger: slog.Default()})

	r.Empty(listOpen(), "the sweep resolves the entry of the deleted check")
}
