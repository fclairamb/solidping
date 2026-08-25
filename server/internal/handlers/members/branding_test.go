package members_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/members"
)

const brandingMembersBaseURL = "https://solidping.example"

// newBrandedMemberFixture mirrors newMemberFixture but wires a formatter that
// knows the base URL — without one no absolute logo URL can be built, and the
// branding assertions below would be vacuous.
func newBrandedMemberFixture(ctx context.Context, t *testing.T, slug string, logoURL *string) *memberFixture {
	t.Helper()

	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization(slug, "Acme Corp")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	if logoURL != nil {
		r.NoError(dbSvc.UpdateOrganization(ctx, org.UID, models.OrganizationUpdate{LogoURL: logoURL}))
		org.LogoURL = logoURL
	}

	mailer := &capturingEmailSender{}

	formatter, err := email.NewFormatter(email.WithBaseURL(brandingMembersBaseURL))
	r.NoError(err)

	svc := members.NewService(dbSvc,
		members.WithEmailSender(mailer),
		members.WithEmailFormatter(formatter),
		members.WithAppBaseURL(brandingMembersBaseURL))

	return &memberFixture{svc: svc, dbSvc: dbSvc, org: org, mailer: mailer}
}

// TestPagingNudgeCarriesTheOrgLogo is the end-to-end check for one of
// email.ApplyOrgBranding's six call sites: the org's stored logo really does
// reach the rendered message, made absolute, with the org named in the header
// alt text and the footer attribution.
func TestPagingNudgeCarriesTheOrgLogo(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	logo := "/pub/assets/org-logo-uid"
	fx := newBrandedMemberFixture(ctx, t, "nudge-branded", &logo)

	_, member := fx.addMember(ctx, t, "colleague@acme.test", "Colleague")

	r.NoError(fx.svc.SendPagingNudge(ctx, fx.org.Slug, member.UID))
	r.Equal(1, fx.mailer.sent)

	html := fx.mailer.last.HTML
	r.Contains(html, brandingMembersBaseURL+"/pub/assets/org-logo-uid")
	r.Contains(html, `alt="Acme Corp"`)
	r.Contains(html, "Acme Corp — sent by SolidPing")
	r.NotContains(html, "/dash0/logo.png")
}

// TestPagingNudgeFallsBackToTheProductLogo is the positive control for the
// test above: the same path, an org with no logo, and the SolidPing logo
// renders instead — so a green suite cannot mean "no logo is ever rendered".
func TestPagingNudgeFallsBackToTheProductLogo(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	fx := newBrandedMemberFixture(ctx, t, "nudge-unbranded", nil)

	_, member := fx.addMember(ctx, t, "colleague@acme.test", "Colleague")

	r.NoError(fx.svc.SendPagingNudge(ctx, fx.org.Slug, member.UID))
	r.Equal(1, fx.mailer.sent)

	html := fx.mailer.last.HTML
	r.Contains(html, brandingMembersBaseURL+"/dash0/logo.png")
	r.Contains(html, `alt="SolidPing"`)
	r.Contains(html, "Acme Corp — sent by SolidPing")
}
