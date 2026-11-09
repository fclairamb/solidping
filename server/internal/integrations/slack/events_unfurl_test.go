package slack

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// unfurlTexts flattens an unfurl's blocks (section text, then context text).
func unfurlTexts(unfurl *Unfurl) []string {
	texts := make([]string, 0, len(unfurl.Blocks))
	for _, block := range unfurl.Blocks {
		if block.Text != nil {
			texts = append(texts, block.Text.Text)
		}

		for _, el := range block.Elements {
			if ce, ok := el.(ContextElement); ok {
				texts = append(texts, ce.Text)
			}
		}
	}

	return texts
}

// TestBuildLinkUnfurlResolvesCheckAndIncident pins issue #769 and its follow-up:
// check and incident links unfurl to a card naming the entity with its current
// state and details, and stay generic for another organization's links or
// unknown entities.
func TestBuildLinkUnfurlResolvesCheckAndIncident(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h, svc, org := solidpingSetup(t)
	ctx := t.Context()
	now := time.Now()

	chk := models.NewCheck(org.UID, "vault-2", "http")
	name := "Vault <nonprod> vault-2"
	chk.Name = &name
	chk.Status = models.CheckStatusUp
	changed := now.Add(-3 * time.Hour)
	chk.StatusChangedAt = &changed
	last := now.Add(-20 * time.Second)
	chk.LastResultAt = &last
	r.NoError(svc.db.CreateCheck(ctx, chk))

	// 3 up + 1 down in the last 24h = 75%; one older down is out of the window.
	for i, status := range []models.ResultStatus{
		models.ResultStatusUp, models.ResultStatusUp, models.ResultStatusUp, models.ResultStatusDown,
	} {
		res := models.NewResult(org.UID, chk.UID, status, 10)
		res.PeriodStart = now.Add(-time.Duration(i+1) * time.Hour)
		r.NoError(svc.db.CreateResult(ctx, res))
	}

	old := models.NewResult(org.UID, chk.UID, models.ResultStatusDown, 10)
	old.PeriodStart = now.Add(-30 * time.Hour)
	r.NoError(svc.db.CreateResult(ctx, old))

	incident := models.NewIncident(org.UID, chk.UID, now.Add(-12*time.Minute), "")
	incident.Title = nil
	r.NoError(svc.db.CreateIncident(ctx, incident))
	stored, err := svc.db.GetIncident(ctx, org.UID, incident.UID)
	r.NoError(err)
	r.NotZero(stored.Number)

	texts := func(u string) []string {
		unfurl := h.buildLinkUnfurlAt(ctx, msgTeamID, u, now)
		r.NotNil(unfurl)

		return unfurlTexts(unfurl)
	}

	base := "http://localhost:4000/d/orgs/" + org.Slug
	checkURL := base + "/checks/" + chk.UID
	got := texts(checkURL)
	r.Len(got, 2)
	r.Equal(":large_green_circle: <"+checkURL+"|Vault &lt;nonprod&gt; vault-2> · *Up* for 3h", got[0])
	r.Equal("HTTP · every 1m · 75.00% uptime (24h) · last checked just now", got[1])

	incURL := base + "/incidents/" + incident.UID
	got = texts(incURL)
	r.Len(got, 2)
	r.Contains(got[0], ":rotating_light: <"+incURL+"|#")
	r.Contains(got[0], "· Vault &lt;nonprod&gt; vault-2>")
	r.Equal("Ongoing for 12m", got[1])

	generic := func(u string) {
		r.Equal([]string{"*SolidPing Link*\n<" + u + "|View in SolidPing>"}, texts(u))
	}
	generic("http://localhost:4000/d/orgs/other-org/checks/" + chk.UID)
	generic(base + "/checks/does-not-exist")
	generic(base + "/incidents/does-not-exist")
	generic(base + "/settings")
	generic("http://localhost:4000/pricing")
}

func TestCheckUnfurlState(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Now()
	recent := now.Add(-time.Second)
	check := func(status models.CheckStatus, enabled bool) *models.Check {
		return &models.Check{Status: status, Enabled: enabled, LastResultAt: &recent}
	}

	cases := []struct {
		check *models.Check
		emoji string
		state string
	}{
		{check(models.CheckStatusUp, false), ":double_vertical_bar:", "Paused"},
		{check(models.CheckStatusStale, true), ":white_circle:", "No recent data"},
		{check(models.CheckStatusUp, true), ":large_green_circle:", "Up"},
		{check(models.CheckStatusDown, true), ":red_circle:", "Down"},
		{check(models.CheckStatusDegraded, true), ":large_yellow_circle:", "Degraded"},
		{check(models.CheckStatusWarning, true), ":large_yellow_circle:", "Warning"},
		{check(models.CheckStatusValidating, true), ":large_orange_circle:", "Confirming failure"},
		{check(models.CheckStatusCreated, true), ":white_circle:", "Waiting for first result"},
	}

	for _, tc := range cases {
		emoji, state := checkUnfurlState(tc.check, now)
		r.Equal(tc.emoji, emoji, tc.state)
		r.Equal(tc.state, state)
	}
}

func TestIncidentUnfurlDetails(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Now()
	acked := now.Add(-time.Minute)
	resolved := now.Add(-2 * time.Hour)

	active := &models.Incident{State: models.IncidentStateActive, StartedAt: now.Add(-5 * time.Minute)}
	r.Equal(":rotating_light:", incidentUnfurlEmoji(active))
	r.Equal("Ongoing for 5m", incidentUnfurlDetails(active, now))

	active.AcknowledgedAt = &acked
	r.Equal("Ongoing for 5m · acknowledged", incidentUnfurlDetails(active, now))

	done := &models.Incident{
		State: models.IncidentStateResolved, StartedAt: resolved.Add(-14 * time.Minute), ResolvedAt: &resolved,
	}
	r.Equal(":white_check_mark:", incidentUnfurlEmoji(done))
	r.Equal("Resolved 2h ago · lasted 14m", incidentUnfurlDetails(done, now))
}

func TestFormatUnfurlUptime(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("100%", formatUnfurlUptime(100))
	r.Equal("99.99%", formatUnfurlUptime(99.9999))
	r.Equal("75.00%", formatUnfurlUptime(75))
	r.Equal("0.00%", formatUnfurlUptime(0))
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
