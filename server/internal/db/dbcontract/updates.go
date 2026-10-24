package dbcontract

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

func ptr[T any](v T) *T { return &v }

// runCheckUpdatesAndFilters covers every optional column of UpdateCheck and
// every ListChecks filter, including the clear-to-NULL transitions.
//
//nolint:funlen // linear contract scenario
func runCheckUpdatesAndFilters(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	group := models.NewCheckGroup(fix.org.UID, "Upd group", "upd-group")
	must.NoError(svc.CreateCheckGroup(ctx, group))

	chk := models.NewCheck(fix.org.UID, "upd-check", "http")
	must.NoError(svc.CreateCheck(ctx, chk))
	label, err := svc.GetOrCreateLabel(ctx, fix.org.UID, "env", "prod")
	must.NoError(err)
	must.NoError(svc.SetCheckLabels(ctx, chk.UID, []string{label.UID}))

	period := timeutils.Duration(30 * time.Second)
	spread := timeutils.Duration(5 * time.Second)
	must.NoError(svc.UpdateCheck(ctx, chk.UID, &models.CheckUpdate{
		CheckGroupUID:           &group.UID,
		Description:             ptr("described"),
		Type:                    ptr("tcp"),
		ConfigSealed:            ptr("sealed"),
		Internal:                ptr(true),
		FailQuorum:              ptr("majority"),
		RegionSpread:            &spread,
		Period:                  &period,
		Placement:               ptr("pinned"),
		Regions:                 &[]string{"eu"},
		RegionCount:             ptr(2),
		RegionPool:              &[]string{"eu", "us"},
		ConfigPrivate:           ptr("priv"),
		Enabled:                 ptr(true),
		TracerouteOnFailure:     ptr(true),
		ReminderIntervalSeconds: ptr(600),
	}))

	got, err := svc.GetCheck(ctx, fix.org.UID, chk.UID)
	must.NoError(err)
	must.Equal("described", *got.Description)
	must.Equal("tcp", got.Type)
	must.True(got.Internal)
	must.NotNil(got.CheckGroupUID)

	// Clear transitions.
	must.NoError(svc.UpdateCheck(ctx, chk.UID, &models.CheckUpdate{
		CheckGroupUID:            ptr(""),
		ClearConfigPrivate:       true,
		ClearConfigSealed:        true,
		ClearFailQuorum:          true,
		ClearRegionSpread:        true,
		ClearRegionCount:         true,
		ClearRegionPool:          true,
		ClearTracerouteOnFailure: true,
		ClearReminderInterval:    true,
	}))
	got, err = svc.GetCheck(ctx, fix.org.UID, chk.UID)
	must.NoError(err)
	must.Nil(got.CheckGroupUID)
	must.Nil(got.RegionCount)

	// Re-assign the group, set value forms of the remaining knobs.
	must.NoError(svc.UpdateCheck(ctx, chk.UID, &models.CheckUpdate{
		CheckGroupUID: &group.UID,
		Internal:      ptr(true),
	}))

	list := func(f *models.ListChecksFilter) []*models.Check {
		t.Helper()
		items, _, listErr := svc.ListChecks(ctx, fix.org.UID, f)
		must.NoError(listErr)

		return items
	}

	byGroup := list(&models.ListChecksFilter{CheckGroupUID: &group.UID, Internal: ptr("all")})
	must.Len(byGroup, 1)
	must.Equal(chk.UID, byGroup[0].UID)

	none := "none"
	for _, c := range list(&models.ListChecksFilter{CheckGroupUID: &none}) {
		must.Nil(c.CheckGroupUID)
	}

	internalOnly := list(&models.ListChecksFilter{Internal: ptr("true")})
	must.NotEmpty(internalOnly)
	for _, c := range internalOnly {
		must.True(c.Internal)
	}

	must.NotEmpty(list(&models.ListChecksFilter{Internal: ptr("all")}))

	byLabel := list(&models.ListChecksFilter{Labels: map[string]string{"env": "prod"}, Internal: ptr("all")})
	uids := make([]string, 0, len(byLabel))
	for _, c := range byLabel {
		uids = append(uids, c.UID)
	}
	must.Contains(uids, chk.UID)

	byStatus := list(&models.ListChecksFilter{
		Statuses: []models.CheckStatus{models.CheckStatusUp, models.CheckStatusDown}, Internal: ptr("all"),
	})
	for _, c := range byStatus {
		must.Contains([]models.CheckStatus{models.CheckStatusUp, models.CheckStatusDown}, c.Status)
	}
}

// runSLOAndStatusPageUpdates covers the optional columns of UpdateSLO,
// UpdateSLOAlertPolicy and UpdateStatusPage.
//
//nolint:funlen // linear contract scenario
func runSLOAndStatusPageUpdates(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	slo := models.NewSLO(fix.org.UID, "Upd SLO", "upd-slo", 99)
	slo.CheckUID = &fix.check.UID
	must.NoError(svc.CreateSLO(ctx, slo))

	must.NoError(svc.UpdateSLO(ctx, slo.UID, models.SLOUpdate{
		Name:               ptr("Renamed"),
		Slug:               ptr("renamed"),
		Timezone:           ptr("Europe/Paris"),
		ExcludeMaintenance: ptr(true),
		Enabled:            ptr(false),
	}))
	gotSLO, err := svc.GetSLO(ctx, fix.org.UID, slo.UID)
	must.NoError(err)
	must.Equal("Renamed", gotSLO.Name)
	must.False(gotSLO.Enabled)

	policy := models.NewSLOAlertPolicy(fix.org.UID, slo.UID, &models.SLOAlertPolicyDefault{
		Kind: "slow", LongWindowSeconds: 21600, ShortWindowSeconds: 1800, Threshold: 6, Severity: "warning",
	})
	must.NoError(svc.CreateSLOAlertPolicy(ctx, policy))

	now := time.Now().UTC()
	must.NoError(svc.UpdateSLOAlertPolicy(ctx, policy.UID, &models.SLOAlertPolicyUpdate{
		LongWindowSeconds:   ptr(7200),
		ShortWindowSeconds:  ptr(600),
		Severity:            ptr("critical"),
		MinSamples:          ptr(5),
		LastEvaluatedAt:     &now,
		LastLongBurnRate:    ptr(1.5),
		LastShortBurnRate:   ptr(2.5),
		BelowThresholdSince: &now,
	}))
	gotPolicy, err := svc.GetSLOAlertPolicy(ctx, fix.org.UID, policy.UID)
	must.NoError(err)
	must.Equal(7200, gotPolicy.LongWindowSeconds)
	must.Equal(5, gotPolicy.MinSamples)

	must.NoError(svc.UpdateSLOAlertPolicy(ctx, policy.UID, &models.SLOAlertPolicyUpdate{
		ClearBelowThresholdSince: true,
		ClearLastBurnRates:       true,
	}))
	gotPolicy, err = svc.GetSLOAlertPolicy(ctx, fix.org.UID, policy.UID)
	must.NoError(err)
	must.Nil(gotPolicy.BelowThresholdSince)
	must.Nil(gotPolicy.LastLongBurnRate)

	page := models.NewStatusPage(fix.org.UID, "Upd page", "upd-page")
	must.NoError(svc.CreateStatusPage(ctx, page))
	must.NoError(svc.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{
		Name:             ptr("Upd page 2"),
		Slug:             ptr("upd-page-2"),
		Description:      ptr("desc"),
		Visibility:       ptr("private"),
		IsDefault:        ptr(false),
		Enabled:          ptr(true),
		ShowAvailability: ptr(false),
		ShowResponseTime: ptr(false),
		HistoryDays:      ptr(30),
		HistoryPeriod:    ptr("day"),
		Language:         ptr("fr"),
		AutoPublish:      ptr(true),
		PublishDegraded:  ptr(true),
		CustomCSS:        ptr("body{}"),
		HideBranding:     ptr(true),
		PasswordHash:     ptr("hash"),
		KioskTokenHash:   ptr("khash"),
	}))
	gotPage, err := svc.GetStatusPage(ctx, fix.org.UID, page.UID)
	must.NoError(err)
	must.Equal("Upd page 2", gotPage.Name)
	must.Equal("fr", *gotPage.Language)

	// The empty string clears the three-state columns.
	must.NoError(svc.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{
		CustomCSS: ptr(""), PasswordHash: ptr(""), KioskTokenHash: ptr(""),
		Settings: &models.StatusPageSettings{},
	}))
}

// runStateEntries covers the state storage helpers.
func runStateEntries(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc
	org := &fix.org.UID

	value := models.JSONMap{"n": 1}
	entry, created, err := svc.GetOrCreateStateEntry(ctx, org, "contract/key", &value, nil)
	must.NoError(err)
	must.True(created)
	must.NotNil(entry)

	again, created, err := svc.GetOrCreateStateEntry(ctx, org, "contract/key", &models.JSONMap{"n": 2}, nil)
	must.NoError(err)
	must.False(created)
	must.Equal(entry.UID, again.UID)

	ttl := time.Hour
	_, created, err = svc.GetOrCreateStateEntry(ctx, nil, "contract/global", &value, &ttl)
	must.NoError(err)
	must.True(created)

	entries, err := svc.ListStateEntries(ctx, org, "contract/")
	must.NoError(err)
	must.Len(entries, 1)

	deleted, err := svc.DeleteStateEntry(ctx, org, "contract/key")
	must.NoError(err)
	must.True(deleted)
	deleted, err = svc.DeleteStateEntry(ctx, org, "contract/key")
	must.NoError(err)
	must.False(deleted)
}
