package sqlite

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestIncidentPublicationsMigrationSQLite is the SQLite twin of
// incident_publications_migration_postgres_test.go: it proves 017 applies on
// this dialect too and that the same three DDL responsibilities hold — the
// partial unique index (which is what makes the debounce job idempotent), the
// nullable status_updates.author_uid produced by the table rebuild, and the
// auto-publish column defaults.
func TestIncidentPublicationsMigrationSQLite(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(svc.Initialize(ctx))
	t.Cleanup(func() { _ = svc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(svc.CreateOrganization(ctx, org))

	// A row written WITHOUT the application create path — the shape an upgraded
	// installation's existing pages have.
	_, err = svc.DB().NewRaw(
		`insert into status_pages (uid, organization_uid, name, slug, visibility, is_default,
		    enabled, show_availability, show_response_time, history_days, history_period, settings)
		 values ('legacy-page', ?, 'Legacy', 'legacy', 'public', 0, 1, 1, 1, 90, '90d', '{}')`,
		org.UID,
	).Exec(ctx)
	r.NoError(err)

	page, err := svc.GetStatusPage(ctx, org.UID, "legacy-page")
	r.NoError(err)
	r.False(page.AutoPublish,
		"the migration must leave pre-existing pages opted OUT on SQLite too")
	r.Equal(models.DefaultAutoPublishDelaySeconds, page.AutoPublishDelaySeconds)
	r.Equal(string(models.AutoResolveIfUntouched), page.AutoResolve)

	check := models.NewCheck(org.UID, "payments-api", "http")
	r.NoError(svc.CreateCheck(ctx, check))

	incident := models.NewIncident(org.UID, check.UID, page.CreatedAt, "payments-api is down")
	r.NoError(svc.CreateIncident(ctx, incident))

	first := models.NewIncidentPublication(org.UID, page.UID, "Payments API is experiencing issues",
		page.CreatedAt)
	first.IncidentUID = &incident.UID
	first.AutoCreated = true
	r.NoError(svc.CreateIncidentPublication(ctx, first))

	second := models.NewIncidentPublication(org.UID, page.UID, "Duplicate", page.CreatedAt)
	second.IncidentUID = &incident.UID
	r.Error(svc.CreateIncidentPublication(ctx, second),
		"the partial unique index must refuse a duplicate (incident, page)")

	r.NoError(svc.SoftDeleteIncidentPublication(ctx, first.UID))
	r.NoError(svc.CreateIncidentPublication(ctx, second),
		"after unpublishing, the same incident can be published again")

	// Hand-authored publications (incident_uid NULL) are exempt from the index.
	for range 3 {
		manual := models.NewIncidentPublication(org.UID, page.UID, "Planned work", page.CreatedAt)
		r.NoError(svc.CreateIncidentPublication(ctx, manual))
	}

	// The rebuild left author_uid nullable and kept incident_publication_uid.
	update := models.NewStatusUpdate(org.UID, page.UID, "")
	update.Kind = models.StatusUpdateKindInvestigating
	update.Title = "Payments API is experiencing issues"
	update.BodyMarkdown = "We are investigating an issue affecting Payments API."
	update.IncidentPublicationUID = &second.UID
	r.NoError(svc.CreateStatusUpdate(ctx, update))

	stored, err := svc.GetStatusUpdateByUID(ctx, update.UID)
	r.NoError(err)
	r.Nil(stored.AuthorUID, "a machine-generated update stores a NULL author")
	r.NotNil(stored.IncidentPublicationUID)

	count, err := svc.CountIncidentPublicationsForIncident(ctx, incident.UID)
	r.NoError(err)
	r.Equal(1, count)
}
