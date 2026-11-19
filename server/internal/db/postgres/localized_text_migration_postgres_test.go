package postgres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestLocalizedTextColumnsPostgres pins migration 038 on PostgreSQL: the four
// *_i18n recipes round-trip through the models, a retitle drops the
// publication's recipe, and the backfill gives existing "<name> is down"
// titles theirs.
func TestLocalizedTextColumnsPostgres(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc := newPubPG(t)

	org := models.NewOrganization("acme", "Acme")
	r.NoError(svc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "database", "http")
	r.NoError(svc.CreateCheck(ctx, check))

	// Round trip of a recipe with params.
	probes := 10
	incident := models.NewIncident(org.UID, check.UID, check.CreatedAt, "database is degraded")
	incident.TitleI18n = models.NewLocalizedText("incident.title.degradedFailures",
		&models.LocalizedParams{Name: "database", Probes: &probes})
	r.NoError(svc.CreateIncident(ctx, incident))

	got, err := svc.GetIncident(ctx, org.UID, incident.UID)
	r.NoError(err)
	r.NotNil(got.TitleI18n)
	r.Equal("incident.title.degradedFailures", got.TitleI18n.Key)
	r.Equal("database", got.TitleI18n.Params.Name)
	r.Equal(10, *got.TitleI18n.Params.Probes)

	page := models.NewStatusPage(org.UID, "Status", "status")
	r.NoError(svc.CreateStatusPage(ctx, page))

	pub := models.NewIncidentPublication(org.UID, page.UID, "Database is experiencing issues", check.CreatedAt)
	pub.PublicTitleI18n = models.NewLocalizedText("publication.openedTitle",
		&models.LocalizedParams{Name: "Database"})
	r.NoError(svc.CreateIncidentPublication(ctx, pub))

	gotPub, err := svc.GetIncidentPublication(ctx, org.UID, pub.UID)
	r.NoError(err)
	r.NotNil(gotPub.PublicTitleI18n)
	r.Equal("publication.openedTitle", gotPub.PublicTitleI18n.Key)

	retitled := "Database maintenance"
	r.NoError(svc.UpdateIncidentPublication(ctx, pub.UID, &models.IncidentPublicationUpdate{PublicTitle: &retitled}))
	gotPub, err = svc.GetIncidentPublication(ctx, org.UID, pub.UID)
	r.NoError(err)
	r.Equal(retitled, gotPub.PublicTitle)
	r.Nil(gotPub.PublicTitleI18n, "a retitle must drop the templated title's recipe")

	update := models.NewStatusUpdate(org.UID, page.UID, "")
	update.Kind = models.StatusUpdateKindInvestigating
	update.Title = "Database is experiencing issues"
	update.TitleI18n = models.NewLocalizedText("publication.openedTitle", &models.LocalizedParams{Name: "Database"})
	update.BodyMarkdown = "We are investigating an issue affecting Database."
	update.BodyI18n = models.NewLocalizedText("publication.openedBody", &models.LocalizedParams{Name: "Database"})
	update.IncidentPublicationUID = &pub.UID
	r.NoError(svc.CreateStatusUpdate(ctx, update))

	public, err := svc.ListPublicStatusUpdates(ctx, page.UID, 90)
	r.NoError(err)
	r.Len(public, 1)
	r.NotNil(public[0].TitleI18n)
	r.NotNil(public[0].BodyI18n)
	r.Equal("publication.openedBody", public[0].BodyI18n.Key)

	// Backfill: rows stored before 038 have no recipe. Re-run the file's
	// UPDATE statements over such rows.
	legacy := models.NewIncident(org.UID, check.UID, check.CreatedAt, "payments-api is down")
	unnamed := models.NewIncident(org.UID, check.UID, check.CreatedAt, "Check is down")
	manual := models.NewIncident(org.UID, check.UID, check.CreatedAt, "Planned database failover")
	for _, inc := range []*models.Incident{legacy, unnamed, manual} {
		r.NoError(svc.CreateIncident(ctx, inc))
	}

	content, err := os.ReadFile(filepath.Join("migrations", "038_v0_52_0.tx.up.sql"))
	r.NoError(err)

	ran := 0
	for _, stmt := range strings.Split(string(content), "--bun:split") {
		if strings.HasPrefix(stripSQLComments(stmt), "update incidents") {
			_, err = svc.DB().ExecContext(ctx, stmt)
			r.NoError(err)
			ran++
		}
	}
	r.Equal(2, ran, "the backfill statements were not found in the migration")

	got, err = svc.GetIncident(ctx, org.UID, legacy.UID)
	r.NoError(err)
	r.NotNil(got.TitleI18n)
	r.Equal("incident.title.down", got.TitleI18n.Key)
	r.Equal("payments-api", got.TitleI18n.Params.Name)

	got, err = svc.GetIncident(ctx, org.UID, unnamed.UID)
	r.NoError(err)
	r.NotNil(got.TitleI18n)
	r.Equal("incident.title.downUnnamed", got.TitleI18n.Key)
	r.Nil(got.TitleI18n.Params)

	got, err = svc.GetIncident(ctx, org.UID, manual.UID)
	r.NoError(err)
	r.Nil(got.TitleI18n, "a title a person wrote keeps no recipe")
}

// stripSQLComments drops the full-line `--` comments and surrounding blanks of
// one migration statement.
func stripSQLComments(stmt string) string {
	var kept []string

	for _, line := range strings.Split(stmt, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			kept = append(kept, line)
		}
	}

	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// TestTranslationsColumnsPostgres pins the status-update-translations section
// of migration 038 on PostgreSQL: the columns exist, an existing row has NULL,
// the map round-trips, and the down migration drops them.
func TestTranslationsColumnsPostgres(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc := newPubPG(t)

	org := models.NewOrganization("acme", "Acme")
	r.NoError(svc.CreateOrganization(ctx, org))

	page := models.NewStatusPage(org.UID, "Status", "status")
	r.NoError(svc.CreateStatusPage(ctx, page))

	pub := models.NewIncidentPublication(org.UID, page.UID, "Outage", time.Now())
	r.NoError(svc.CreateIncidentPublication(ctx, pub))

	update := models.NewStatusUpdate(org.UID, page.UID, "")
	update.Kind = models.StatusUpdateKindInfo
	update.Title, update.BodyMarkdown = "Notice", "Body"
	r.NoError(svc.CreateStatusUpdate(ctx, update))

	var nulls int
	r.NoError(svc.DB().NewRaw(
		"select count(*) from status_updates where translations is null and uid = ?", update.UID).Scan(ctx, &nulls))
	r.Equal(1, nulls, "an existing row has NULL translations")

	update.Translations = models.Translations{"fr": {Title: "Avis", Body: "Corps"}}
	r.NoError(svc.UpdateStatusUpdate(ctx, update))
	got, err := svc.GetStatusUpdateByUID(ctx, update.UID)
	r.NoError(err)
	r.Equal(models.Translation{Title: "Avis", Body: "Corps"}, got.Translations["fr"])

	public, err := svc.ListPublicStatusUpdates(ctx, page.UID, 90)
	r.NoError(err)
	r.Len(public, 1)
	r.Equal("Avis", public[0].Translations["fr"].Title)

	r.NoError(svc.UpdateIncidentPublication(ctx, pub.UID, &models.IncidentPublicationUpdate{
		Translations: models.Translations{"de": {Title: "Ausfall"}},
	}))
	gotPub, err := svc.GetIncidentPublication(ctx, org.UID, pub.UID)
	r.NoError(err)
	r.Equal("Ausfall", gotPub.Translations["de"].Title)

	r.NoError(svc.UpdateIncidentPublication(ctx, pub.UID, &models.IncidentPublicationUpdate{ClearTranslations: true}))
	gotPub, err = svc.GetIncidentPublication(ctx, org.UID, pub.UID)
	r.NoError(err)
	r.Nil(gotPub.Translations)

	content, err := os.ReadFile(filepath.Join("migrations", "038_v0_52_0.tx.down.sql"))
	r.NoError(err)

	_, section, found := strings.Cut(string(content), "-- SECTION: status-update-translations")
	r.True(found)
	_, err = svc.DB().ExecContext(ctx, section)
	r.NoError(err)

	_, err = svc.DB().ExecContext(ctx, "select translations from status_updates")
	r.Error(err, "the down migration drops the column")
}
