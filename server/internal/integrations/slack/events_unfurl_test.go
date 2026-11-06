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
