package dbcontract

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func runStatusPages(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	checkA := models.NewCheck(fix.org.UID, "sp-check-a", "http")
	checkB := models.NewCheck(fix.org.UID, "sp-check-b", "http")
	must.NoError(svc.CreateCheck(ctx, checkA))
	must.NoError(svc.CreateCheck(ctx, checkB))

	// A page created with its section and resources in one transaction.
	page := models.NewStatusPage(fix.org.UID, "Main", "main")
	page.IsDefault = true
	section := models.NewStatusPageSection(page.UID, "Core", "core", 0)
	res1 := models.NewStatusPageResource(section.UID, checkA.UID, 0)
	res2 := models.NewStatusPageResource(section.UID, checkB.UID, 1)
	must.NoError(svc.CreateStatusPageWithDefaultSection(ctx, page, section, []*models.StatusPageResource{res1, res2}))

	byUIDOrSlug, err := svc.GetStatusPageByUidOrSlug(ctx, fix.org.UID, "main")
	must.NoError(err)
	must.Equal(page.UID, byUIDOrSlug.UID)

	byUIDOrSlug, err = svc.GetStatusPageByUidOrSlug(ctx, fix.org.UID, page.UID)
	must.NoError(err)
	must.Equal(page.UID, byUIDOrSlug.UID)

	def, err := svc.GetDefaultStatusPage(ctx, fix.org.UID)
	must.NoError(err)
	must.Equal(page.UID, def.UID)

	second := models.NewStatusPage(fix.org.UID, "Second", "second")
	must.NoError(svc.CreateStatusPage(ctx, second))

	pages, err := svc.ListStatusPages(ctx, fix.org.UID)
	must.NoError(err)
	must.Len(pages, 2)

	statusPageDomainAndUpdates(t, fix, page, second)
	statusPageSections(t, fix, page, section)
	statusPageResources(t, fix, section, res1, res2, checkA)

	must.NoError(svc.DeleteStatusPage(ctx, second.UID))

	_, err = svc.GetStatusPage(ctx, fix.org.UID, second.UID)
	must.Error(err)
}

func statusPageDomainAndUpdates(t *testing.T, fix *fixture, page, second *models.StatusPage) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	// Custom domain lifecycle.
	domain := "status.acme.com"
	token := "tok"
	must.NoError(svc.UpdateStatusPageCustomDomain(ctx, second.UID, &models.StatusPageCustomDomainUpdate{
		Domain: &domain, Token: &token, State: models.CustomDomainStatePending,
	}))

	byDomain, err := svc.GetStatusPageByCustomDomain(ctx, domain)
	must.NoError(err)
	must.Equal(second.UID, byDomain.UID)

	withDomain, err := svc.ListStatusPagesWithCustomDomain(ctx)
	must.NoError(err)
	must.NotEmpty(withDomain)

	count, err := svc.CountStatusPagesWithCustomDomain(ctx, fix.org.UID)
	must.NoError(err)
	must.Equal(1, count)

	// Page updates, including the branding section and kiosk-style columns.
	history := 30
	desc := "all systems"
	autoPublish := false
	css := "body{color:red}"
	must.NoError(svc.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{
		HistoryDays: &history, Description: &desc, AutoPublish: &autoPublish, CustomCSS: &css,
	}))

	logo := "file-1"
	must.NoError(svc.UpdateStatusPageBranding(ctx, page.UID, &models.StatusPageBrandingUpdate{
		LogoFileUID: &logo, HideBranding: true,
	}))

	got, err := svc.GetStatusPage(ctx, fix.org.UID, page.UID)
	must.NoError(err)
	must.Equal(30, got.HistoryDays)
	must.False(got.AutoPublish)
}

func statusPageSections(t *testing.T, fix *fixture, page *models.StatusPage, section *models.StatusPageSection) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	// Sections.
	maxPos, err := svc.MaxStatusPageSectionPosition(ctx, page.UID)
	must.NoError(err)

	extra := models.NewStatusPageSection(page.UID, "Extra", "extra", maxPos+1)
	must.NoError(svc.CreateStatusPageSection(ctx, extra))

	bySlug, err := svc.GetStatusPageSectionBySlug(ctx, page.UID, "extra")
	must.NoError(err)
	must.Equal(extra.UID, bySlug.UID)

	newName := "Extras"
	newPos := 5
	must.NoError(svc.UpdateStatusPageSection(ctx, extra.UID, &models.StatusPageSectionUpdate{
		Name: &newName, Position: &newPos,
		SetSelector: true, Selector: &models.SectionSelector{All: true},
	}))
	must.NoError(svc.UpdateStatusPageSection(ctx, extra.UID, &models.StatusPageSectionUpdate{SetSelector: true}))

	must.NoError(svc.ReorderStatusPageSections(ctx, page.UID, []string{extra.UID, section.UID}))

	sections, err := svc.ListStatusPageSections(ctx, page.UID)
	must.NoError(err)
	must.Len(sections, 2)
	must.Equal(extra.UID, sections[0].UID)

	must.NoError(svc.DeleteStatusPageSection(ctx, extra.UID))
}

func statusPageResources(
	t *testing.T, fix *fixture, section *models.StatusPageSection,
	res1, res2 *models.StatusPageResource, checkA *models.Check,
) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	// Resources.
	maxRes, err := svc.MaxStatusPageResourcePosition(ctx, section.UID)
	must.NoError(err)
	must.Equal(1, maxRes)

	must.NoError(svc.ReorderStatusPageResources(ctx, section.UID, []string{res2.UID, res1.UID}))

	resources, err := svc.ListStatusPageResources(ctx, section.UID)
	must.NoError(err)
	must.Len(resources, 2)
	must.Equal(res2.UID, resources[0].UID)

	publicName := "Check A"
	must.NoError(svc.UpdateStatusPageResource(ctx, section.UID, res1.UID, &models.StatusPageResourceUpdate{
		PublicName: &publicName,
	}))

	targets, err := svc.ListStatusPageTargetsForCheck(ctx, checkA.UID, nil)
	must.NoError(err)
	must.NotEmpty(targets)

	must.NoError(svc.DeleteStatusPageResource(ctx, section.UID, res1.UID))
}

func runCheckConnections(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	check := models.NewCheck(fix.org.UID, "conn-check", "http")
	must.NoError(svc.CreateCheck(ctx, check))

	hook := models.NewIntegration(fix.org.UID, models.ConnectionTypeWebhook, "hook")
	hook.Settings = models.JSONMap{"team_id": "T-contract"}
	slack := models.NewIntegration(fix.org.UID, models.ConnectionTypeSlack, "slack")
	slack.Settings = models.JSONMap{"team_id": "T-contract"}
	must.NoError(svc.CreateChannel(ctx, hook))
	must.NoError(svc.CreateChannel(ctx, slack))

	byProp, err := svc.GetChannelByProperty(ctx, string(models.ConnectionTypeSlack), "team_id", "T-contract")
	must.NoError(err)
	must.Equal(slack.UID, byProp.UID)

	must.NoError(svc.SetCheckConnections(ctx, check.UID, []string{hook.UID, slack.UID}))

	conns, err := svc.ListCheckConnectionsWithSettings(ctx, check.UID)
	must.NoError(err)
	must.Len(conns, 2)

	settings := models.JSONMap{"channel": "#ops"}
	must.NoError(svc.UpdateCheckConnection(ctx, check.UID, hook.UID, &models.CheckConnectionUpdate{Settings: &settings}))

	one, err := svc.GetCheckConnection(ctx, check.UID, hook.UID)
	must.NoError(err)
	must.NotNil(one.Settings)

	must.NoError(svc.DeleteCheckConnection(ctx, check.UID, hook.UID))

	_, err = svc.GetCheckConnection(ctx, check.UID, hook.UID)
	must.Error(err)

	// Replacing the set with an empty one clears it.
	must.NoError(svc.SetCheckConnections(ctx, check.UID, nil))

	conns, err = svc.ListCheckConnectionsWithSettings(ctx, check.UID)
	must.NoError(err)
	must.Empty(conns)

	found, err := svc.GetCheckAny(ctx, check.UID)
	must.NoError(err)
	must.Equal(check.UID, found.UID)
}
