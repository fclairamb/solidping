package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portIncidentPublications is distinct from every other _postgres_test.go
// embedded port in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portIncidentPublications = 15481

func newPubPG(t *testing.T) *Service {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	svc, err := New(ctx, &Config{Embedded: true, Port: portIncidentPublications, RunMode: "test"})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	return svc
}

// TestIncidentPublicationsMigrationPostgres proves 017 applies on the Postgres
// dialect and that the three behaviors the DDL is responsible for actually
// hold there: the partial unique index (which is what makes the debounce job
// idempotent), the nullable status_updates.author_uid (a machine post has no
// author), and the auto-publish column defaults.
func TestIncidentPublicationsMigrationPostgres(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc := newPubPG(t)
	ctx := t.Context()

	org := models.NewOrganization("acme", "Acme")
	r.NoError(svc.CreateOrganization(ctx, org))

	// A row written WITHOUT the application create path — the shape an upgraded
	// installation's existing pages have.
	//nolint:dupword // repeated SQL boolean literals, not prose.
	const legacyPageInsert = `insert into status_pages (uid, organization_uid, name, slug,
		    visibility, is_default, enabled, show_availability, show_response_time,
		    history_days, history_period, settings)
		 values (gen_random_uuid(), ?, 'Legacy', 'legacy', 'public', false, true, true, true,
		    90, '90d', '{}')`

	_, err := svc.DB().NewRaw(legacyPageInsert, org.UID).Exec(ctx)
	r.NoError(err)

	pages, err := svc.ListStatusPages(ctx, org.UID)
	r.NoError(err)
	r.Len(pages, 1)
	r.False(pages[0].AutoPublish,
		"the migration must leave pre-existing pages opted OUT on Postgres too")
	r.Equal(models.DefaultAutoPublishDelaySeconds, pages[0].AutoPublishDelaySeconds)
	r.Equal(string(models.AutoResolveIfUntouched), pages[0].AutoResolve)

	page := pages[0]

	check := models.NewCheck(org.UID, "payments-api", "http")
	r.NoError(svc.CreateCheck(ctx, check))

	incident := models.NewIncident(org.UID, check.UID, pages[0].CreatedAt, "payments-api is down")
	r.NoError(svc.CreateIncident(ctx, incident))

	first := models.NewIncidentPublication(org.UID, page.UID, "Payments API is experiencing issues",
		page.CreatedAt)
	first.IncidentUID = &incident.UID
	first.AutoCreated = true
	r.NoError(svc.CreateIncidentPublication(ctx, first))

	// The partial unique index: a second live publication of the same incident
	// on the same page must be refused, so a racing job fire and a manual
	// publish cannot mint two public incidents.
	second := models.NewIncidentPublication(org.UID, page.UID, "Duplicate", page.CreatedAt)
	second.IncidentUID = &incident.UID
	r.Error(svc.CreateIncidentPublication(ctx, second),
		"the partial unique index must refuse a duplicate (incident, page)")

	// Soft-deleting the first frees the slot again — unpublish/republish must
	// work.
	r.NoError(svc.SoftDeleteIncidentPublication(ctx, first.UID))
	r.NoError(svc.CreateIncidentPublication(ctx, second),
		"after unpublishing, the same incident can be published again")

	// Hand-authored publications (incident_uid NULL) are exempt from the index.
	for range 3 {
		manual := models.NewIncidentPublication(org.UID, page.UID, "Planned work", page.CreatedAt)
		r.NoError(svc.CreateIncidentPublication(ctx, manual))
	}

	// status_updates.author_uid is nullable: an auto-generated update has no
	// author, and attributing it to a human would be a lie the UI renders.
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

	// The retention-guard primitive counts live publications only.
	count, err := svc.CountIncidentPublicationsForIncident(ctx, incident.UID)
	r.NoError(err)
	r.Equal(1, count)
}
