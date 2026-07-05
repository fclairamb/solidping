package emailsuppressions_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/emailsuppressions"
)

type testFixture struct {
	dbSvc db.Service
	org   *models.Organization
	check *models.Check
}

func newTestFixture(t *testing.T) *testFixture {
	t.Helper()

	r := require.New(t)

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	r.NoError(dbSvc.Initialize(t.Context()))

	org := models.NewOrganization("esup-api-org", "")
	r.NoError(dbSvc.CreateOrganization(t.Context(), org))

	check := models.NewCheck(org.UID, "esup-api-check", "http")
	r.NoError(dbSvc.CreateCheck(t.Context(), check))

	return &testFixture{dbSvc: dbSvc, org: org, check: check}
}

func TestService_List_Empty(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	svc := emailsuppressions.NewService(f.dbSvc)

	rows, err := svc.List(t.Context(), f.org.Slug)
	r.NoError(err)
	r.Empty(rows)
}

func TestService_List_ResolvesCheckName(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	svc := emailsuppressions.NewService(f.dbSvc)

	sup := models.NewEmailSuppression(f.org.UID, "alice@x.test", &f.check.UID, models.EmailSuppressionSourceLink)
	r.NoError(f.dbSvc.CreateEmailSuppression(t.Context(), sup))

	rows, err := svc.List(t.Context(), f.org.Slug)
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal("alice@x.test", rows[0].Email)
	r.Equal(f.check.UID, rows[0].CheckUID)
	// models.NewCheck only sets Slug (Name stays nil) — the service falls
	// back to Slug for display, matching notifications/email.go's own
	// checkName fallback logic.
	r.Equal(*f.check.Slug, rows[0].CheckName)
	r.Equal(models.EmailSuppressionSourceLink, rows[0].Source)
	r.NotEmpty(rows[0].CreatedAt)
}

func TestService_List_OrgWideOmitsCheckFields(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	svc := emailsuppressions.NewService(f.dbSvc)

	sup := models.NewEmailSuppression(f.org.UID, "bob@x.test", nil, models.EmailSuppressionSourceLink)
	r.NoError(f.dbSvc.CreateEmailSuppression(t.Context(), sup))

	rows, err := svc.List(t.Context(), f.org.Slug)
	r.NoError(err)
	r.Len(rows, 1)
	r.Empty(rows[0].CheckUID, "org-wide suppression must have no check UID")
	r.Empty(rows[0].CheckName)
}

func TestService_List_UnknownOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	svc := emailsuppressions.NewService(f.dbSvc)

	_, err := svc.List(t.Context(), "does-not-exist")
	r.ErrorIs(err, emailsuppressions.ErrOrganizationNotFound)
}

func TestService_Delete(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	svc := emailsuppressions.NewService(f.dbSvc)

	sup := models.NewEmailSuppression(f.org.UID, "delete-me@x.test", &f.check.UID, models.EmailSuppressionSourceLink)
	r.NoError(f.dbSvc.CreateEmailSuppression(t.Context(), sup))

	r.NoError(svc.Delete(t.Context(), f.org.Slug, sup.UID))

	rows, err := svc.List(t.Context(), f.org.Slug)
	r.NoError(err)
	r.Empty(rows)
}

// TestService_Delete_WrongOrgRejected confirms a suppression can't be
// deleted through a different org's slug — the read-before-delete check
// scopes ownership, so a UID from org A is not deletable via org B's path.
func TestService_Delete_WrongOrgRejected(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	svc := emailsuppressions.NewService(f.dbSvc)

	otherOrg := models.NewOrganization("esup-api-other", "")
	r.NoError(f.dbSvc.CreateOrganization(t.Context(), otherOrg))

	sup := models.NewEmailSuppression(f.org.UID, "victim@x.test", &f.check.UID, models.EmailSuppressionSourceLink)
	r.NoError(f.dbSvc.CreateEmailSuppression(t.Context(), sup))

	err := svc.Delete(t.Context(), otherOrg.Slug, sup.UID)
	r.ErrorIs(err, emailsuppressions.ErrSuppressionNotFound)

	rows, err := svc.List(t.Context(), f.org.Slug)
	r.NoError(err)
	r.Len(rows, 1, "the suppression must remain since the delete was rejected")
}

func TestService_Delete_UnknownUID(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	svc := emailsuppressions.NewService(f.dbSvc)

	err := svc.Delete(t.Context(), f.org.Slug, "does-not-exist-uid")
	r.ErrorIs(err, emailsuppressions.ErrSuppressionNotFound)
}
