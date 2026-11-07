package slack

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestBuildLinkUnfurlResolvesCheckAndIncident pins issue #769: check and
// incident links unfurl to a one-line preview naming the entity (distinct emoji
// per type) instead of the same generic "SolidPing Link" card, and stay generic
// for another organization's links or unknown entities.
func TestBuildLinkUnfurlResolvesCheckAndIncident(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h, svc, org := solidpingSetup(t)
	ctx := t.Context()

	chk := models.NewCheck(org.UID, "vault-2", "http")
	name := "Vault <nonprod> vault-2"
	chk.Name = &name
	r.NoError(svc.db.CreateCheck(ctx, chk))

	incident := models.NewIncident(org.UID, chk.UID, time.Now(), "")
	incident.Title = nil
	r.NoError(svc.db.CreateIncident(ctx, incident))
	stored, err := svc.db.GetIncident(ctx, org.UID, incident.UID)
	r.NoError(err)

	text := func(u string) string {
		unfurl := h.buildLinkUnfurl(ctx, msgTeamID, u)
		r.NotNil(unfurl)
		r.Len(unfurl.Blocks, 1)

		return unfurl.Blocks[0].Text.Text
	}

	base := "http://localhost:4000/d/orgs/" + org.Slug
	checkURL := base + "/checks/" + chk.UID
	r.Equal(":mag: <"+checkURL+"|Vault &lt;nonprod&gt; vault-2>", text(checkURL))

	incURL := base + "/incidents/" + incident.UID
	r.Contains(text(incURL), ":rotating_light: <"+incURL+"|#")
	r.Contains(text(incURL), "· Vault &lt;nonprod&gt; vault-2>")
	r.NotZero(stored.Number)

	generic := func(u string) {
		r.Equal("*SolidPing Link*\n<"+u+"|View in SolidPing>", text(u))
	}
	generic("http://localhost:4000/d/orgs/other-org/checks/" + chk.UID)
	generic(base + "/checks/does-not-exist")
	generic(base + "/incidents/does-not-exist")
	generic(base + "/settings")
	generic("http://localhost:4000/pricing")
}

// TestBuildLinkUnfurlStaysGenericWhenUnresolvable covers the links that must
// never resolve: an unknown workspace, malformed URLs and non-entity paths.
func TestBuildLinkUnfurlStaysGenericWhenUnresolvable(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h, _, org := solidpingSetup(t)
	ctx := t.Context()

	checkURL := "http://localhost:4000/d/orgs/" + org.Slug + "/checks/abc"
	generic := func(teamID, u string) {
		unfurl := h.buildLinkUnfurl(ctx, teamID, u)
		r.NotNil(unfurl)
		r.Equal("*SolidPing Link*\n<"+u+"|View in SolidPing>", unfurl.Blocks[0].Text.Text)
	}

	generic("T-UNKNOWN", checkURL)
	generic(msgTeamID, "http://localhost:4000/%zz/orgs/x/checks/abc")
	generic(msgTeamID, "http://localhost:4000/other/orgs/"+org.Slug+"/checks/abc")
	generic(msgTeamID, "http://localhost:4000/d/orgs/"+org.Slug+"/status-pages/abc")
	generic(msgTeamID, "http://localhost:4000/d/orgs//checks/abc")

	h.svc = nil
	generic(msgTeamID, checkURL)
}

func TestParseDashboardEntityLink(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	link, ok := parseDashboardEntityLink("https://x.io/dash0/orgs/Acme/incidents/42")
	r.True(ok)
	r.Equal(dashboardEntityLink{orgSlug: "Acme", kind: linkKindIncident, id: "42"}, link)

	_, ok = parseDashboardEntityLink("://bad")
	r.False(ok)
}

func TestUnfurlCheckNameFallbacks(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	empty, slug, name := "", "my-slug", "My name"
	r.Equal("My name", unfurlCheckName(&models.Check{UID: "u", Name: &name, Slug: &slug}))
	r.Equal("my-slug", unfurlCheckName(&models.Check{UID: "u", Name: &empty, Slug: &slug}))
	r.Equal("u", unfurlCheckName(&models.Check{UID: "u", Slug: &empty}))
	r.Equal("u", unfurlCheckName(&models.Check{UID: "u"}))
}

func TestUnfurlIncidentLabel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h, svc, org := solidpingSetup(t)
	ctx := t.Context()

	chk := models.NewCheck(org.UID, "label-check", "http")
	r.NoError(svc.db.CreateCheck(ctx, chk))

	title := "Disk full"
	r.Equal("#7 · Disk full", h.unfurlIncidentLabel(ctx, org.UID,
		&models.Incident{Number: 7, Title: &title, CheckUID: chk.UID}))
	r.Equal("Disk full", h.unfurlIncidentLabel(ctx, org.UID,
		&models.Incident{Title: &title, CheckUID: chk.UID}))
	r.Equal("#3 · label-check", h.unfurlIncidentLabel(ctx, org.UID,
		&models.Incident{Number: 3, CheckUID: chk.UID}))
	r.Equal("label-check", h.unfurlIncidentLabel(ctx, org.UID,
		&models.Incident{CheckUID: chk.UID}))
	r.Equal("#5", h.unfurlIncidentLabel(ctx, org.UID,
		&models.Incident{Number: 5, CheckUID: "missing"}))
	r.Equal("Incident", h.unfurlIncidentLabel(ctx, org.UID,
		&models.Incident{CheckUID: "missing"}))
}
