package feedback

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

// TestReportFiledWhileImpersonatingNamesTheAdmin: a report sent under an
// impersonation token stays attributed to the target (it shows their screen)
// and records the super admin behind it, in the issue body too (spec
// 2026-09-29-03). Positive control: the target's own token records no
// impersonator and the body has no such row.
func TestReportFiledWhileImpersonatingNamesTheAdmin(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	cfg := &config.Config{Auth: config.AuthConfig{
		JWTSecret: "test-jwt-secret", AccessTokenExpiry: time.Hour,
		RefreshTokenExpiry: time.Hour, ImpersonationEnabled: true,
	}}
	authSvc := auth.NewService(dbSvc, cfg.Auth, cfg, nil, nil)

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	admin := models.NewUser("root@acme.com")
	admin.SuperAdmin = true
	r.NoError(dbSvc.CreateUser(ctx, admin))

	target := models.NewUser("alice@acme.com")
	r.NoError(dbSvc.CreateUser(ctx, target))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, target.UID, models.MemberRoleUser)))

	imper, err := authSvc.Impersonate(ctx, admin.UID, target.UID, "acme", auth.Context{})
	r.NoError(err)
	own, err := authSvc.GenerateMCPAccessToken(ctx, target.UID, "acme", nil, "", time.Hour, "")
	r.NoError(err)

	h := &Handler{svc: &Service{db: dbSvc}, auth: authSvc}

	var sub SubmitReportRequest
	h.attachUserIfAuthenticated(ctx, "Bearer "+imper.AccessToken, &sub)
	r.Equal(target.UID, sub.UserUID)
	r.Equal("alice@acme.com", sub.UserEmail)
	r.Equal("root@acme.com", sub.ImpersonatedBy)

	body := BuildIssueBody(&IssueInput{
		UserEmail: sub.UserEmail, ImpersonatedBy: sub.ImpersonatedBy, ReportedAt: time.Unix(0, 0),
	})
	r.Contains(body, "| Impersonated By | root@acme.com |")

	var ownSub SubmitReportRequest
	h.attachUserIfAuthenticated(ctx, "Bearer "+own, &ownSub)
	r.Equal(target.UID, ownSub.UserUID)
	r.Empty(ownSub.ImpersonatedBy)

	body = BuildIssueBody(&IssueInput{UserEmail: ownSub.UserEmail, ReportedAt: time.Unix(0, 0)})
	r.NotContains(body, "Impersonated By")
}
