package feedback

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

// Spec 2026-10-30-02: an authenticated report skips the anonymous screenshot
// admission only when its author is a member of the named org, or a super
// admin. Any other signed-in account is admitted like an anonymous caller.

// TestSubmitReport_NonMemberSpendsAnonBudget is the regression: with the
// anonymous budget spent, a member of org A naming org B is refused and stores
// nothing in org B, while the same user naming org A is stored.
func TestSubmitReport_NonMemberSpendsAnonBudget(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, orgs := newBudgetSQLite(t, "acme-a", "acme-b")
	orgA, orgB := orgs[0], orgs[1]
	svc, _ := newBudgetService(t, dbSvc, 1024, 1)
	user := newMember(t, dbSvc, orgA)

	_, err := svc.SubmitReport(ctx, screenshotReport(orgA.Slug, ""))
	r.NoError(err, "the anonymous report spends the budget of 1")

	_, err = svc.SubmitReport(ctx, screenshotReport(orgB.Slug, user))
	r.ErrorIs(err, ErrReportRateLimited, "a non-member is admitted like an anonymous caller")
	r.Zero(reportBytes(ctx, t, dbSvc, orgB), "no file is stored in the org the user is not a member of")

	_, err = svc.SubmitReport(ctx, screenshotReport(orgA.Slug, user))
	r.NoError(err, "a member's screenshot to its own org skips the anonymous budget")
	r.Equal(int64(2), reportBytes(ctx, t, dbSvc, orgA))

	_, err = svc.SubmitReport(ctx, &SubmitReportRequest{
		URL: "https://example.com/page", OrgSlug: orgB.Slug, UserUID: user,
	})
	r.NoError(err, "a non-member's report without a screenshot is still accepted")
}

// TestSubmitReport_NonMemberNeedsDispatchSlot: with the dispatch cap spent, a
// non-member's screenshot would never be filed, so it is not stored.
func TestSubmitReport_NonMemberNeedsDispatchSlot(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, orgs := newBudgetSQLite(t, "acme-a", "acme-b")
	orgA, orgB := orgs[0], orgs[1]
	svc, _ := newBudgetService(t, dbSvc, 1024, 20)
	user := newMember(t, dbSvc, orgA)

	svc.dispatchWindowStart = time.Now()
	svc.dispatchCount = maxIssueDispatchesPerHour

	_, err := svc.SubmitReport(ctx, screenshotReport(orgB.Slug, user))
	r.ErrorIs(err, ErrReportRateLimited)
	r.Zero(reportBytes(ctx, t, dbSvc, orgB), "no file is stored for a report that cannot be filed")

	_, err = svc.SubmitReport(ctx, screenshotReport(orgA.Slug, user))
	r.NoError(err, "a member's screenshot to its own org is still stored")
	r.Equal(int64(1), reportBytes(ctx, t, dbSvc, orgA))
}

// TestSubmitReport_SuperAdminSkipsAnonBudget: a super admin's report naming an
// org it is not a member of is stored past the anonymous budget.
func TestSubmitReport_SuperAdminSkipsAnonBudget(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, orgs := newBudgetSQLite(t, "acme-a", "acme-b")
	svc, _ := newBudgetService(t, dbSvc, 1024, 1)
	admin := newUser(t, dbSvc, "root@acme.example", true)

	_, err := svc.SubmitReport(ctx, screenshotReport(orgs[0].Slug, ""))
	r.NoError(err)

	req := screenshotReport(orgs[1].Slug, admin)
	req.UserSuperAdmin = true
	_, err = svc.SubmitReport(ctx, req)
	r.NoError(err, "a super admin's screenshot is stored in any org")
	r.Equal(int64(1), reportBytes(ctx, t, dbSvc, orgs[1]))
}

// TestHandlerSubmitReport_NonMemberToken: through the HTTP handler, a valid
// token of a member of org A naming org B answers 429 RATE_LIMITED with the
// anonymous budget spent, and a super admin's token is stored (201): the
// handler passes the super-admin flag on.
func TestHandlerSubmitReport_NonMemberToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, orgs := newBudgetSQLite(t, "acme-a", "acme-b")
	orgA, orgB := orgs[0], orgs[1]
	svc, _ := newBudgetService(t, dbSvc, 1024, 1)

	cfg := &config.Config{Auth: config.AuthConfig{
		JWTSecret: "test-jwt-secret", AccessTokenExpiry: time.Hour, RefreshTokenExpiry: time.Hour,
	}}
	authSvc := auth.NewService(dbSvc, cfg.Auth, cfg, nil, nil)
	handler := NewHandler(svc, authSvc, svc.cfg)

	user := newMember(t, dbSvc, orgA)
	admin := newUser(t, dbSvc, "root@acme.example", true, orgA)

	userToken, err := authSvc.GenerateMCPAccessToken(ctx, user, orgA.Slug, nil, "", time.Hour, "", "")
	r.NoError(err)
	adminToken, err := authSvc.GenerateMCPAccessToken(ctx, admin, orgA.Slug, nil, "", time.Hour, "", "")
	r.NoError(err)

	post := func(orgSlug, token string) *httptest.ResponseRecorder {
		body, contentType := multipartReport(t, orgSlug)
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/mgmt/report", body)
		req.Header.Set("Content-Type", contentType)

		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		rec := httptest.NewRecorder()
		r.NoError(handler.SubmitReport(rec, req))

		return rec
	}

	rec := post(orgA.Slug, "")
	r.Equal(http.StatusCreated, rec.Code, "the anonymous report spends the budget: %s", rec.Body.String())

	rec = post(orgB.Slug, userToken)
	r.Equal(http.StatusTooManyRequests, rec.Code, rec.Body.String())
	r.Contains(rec.Body.String(), "RATE_LIMITED")
	r.Zero(reportBytes(ctx, t, dbSvc, orgB))

	rec = post(orgA.Slug, userToken)
	r.Equal(http.StatusCreated, rec.Code, "a member's report to its org: %s", rec.Body.String())

	rec = post(orgB.Slug, adminToken)
	r.Equal(http.StatusCreated, rec.Code, "a super admin's report to any org: %s", rec.Body.String())
	r.Equal(int64(1), reportBytes(ctx, t, dbSvc, orgB))
}
