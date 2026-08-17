package statuspages

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// mustMarshalJSON renders a value exactly as the API would ship it, so a test
// can assert on what the WIRE payload does (and does not) contain.
func mustMarshalJSON(t *testing.T, v any) string {
	t.Helper()

	raw, err := json.Marshal(v)
	require.NoError(t, err)

	return string(raw)
}

// --- Status page group resources (spec 2026-08-01-03) ---
//
// A status page resource targets EITHER one check OR one check group. A group
// resource renders as ONE public component: rolled-up status, weighted-average
// availability across its enabled members, and maintenance from a group- or
// member-targeted window. Its members are never named publicly.

// seedGroup creates a check group plus the given member checks (status +
// enabled flag per member) and returns the group.
func seedGroup(
	ctx context.Context, t *testing.T, svc *Service, orgUID, slug string,
	members []struct {
		status  models.CheckStatus
		enabled bool
	},
) *models.CheckGroup {
	t.Helper()

	r := require.New(t)

	group := models.NewCheckGroup(orgUID, "Web frontend", slug)
	r.NoError(svc.db.CreateCheckGroup(ctx, group))

	for _, m := range members {
		check := models.NewCheck(orgUID, "", "http")
		check.CheckGroupUID = &group.UID
		check.Status = m.status
		check.Enabled = m.enabled
		r.NoError(svc.db.CreateCheck(ctx, check))
	}

	return group
}

// seedPageWithSection creates a page + one section and returns both.
func seedPageWithSection(
	ctx context.Context, t *testing.T, svc *Service, orgSlug string, historyPeriod string,
) (StatusPageResponse, StatusPageSectionResponse) {
	t.Helper()

	r := require.New(t)

	req := &CreateStatusPageRequest{Name: "Public", Slug: testPublicSlug}
	if historyPeriod != "" {
		req.HistoryPeriod = strPtr(historyPeriod)
	}

	page, err := svc.CreateStatusPage(ctx, orgSlug, req)
	r.NoError(err)

	section, err := svc.CreateSection(ctx, orgSlug, page.UID, CreateSectionRequest{Name: "Core", Slug: "core"})
	r.NoError(err)

	return page, section
}

// TestCreateResource_TargetXOR pins the checkUid-XOR-checkGroupUid rule at the
// service boundary: zero targets and two targets are both rejected with the
// same error (whose message names BOTH fields), while exactly one of either
// kind is accepted and stored on the matching column.
func TestCreateResource_TargetXOR(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)
	page, section := seedPageWithSection(ctx, t, svc, org.Slug, "")

	check := models.NewCheck(org.UID, "api", "http")
	r.NoError(svc.db.CreateCheck(ctx, check))

	group := models.NewCheckGroup(org.UID, "Web frontend", "web-frontend")
	r.NoError(svc.db.CreateCheckGroup(ctx, group))

	// Zero targets.
	_, err := svc.CreateResource(ctx, org.Slug, page.UID, section.UID, CreateResourceRequest{})
	r.ErrorIs(err, ErrResourceTargetInvalid)
	r.Contains(err.Error(), "checkUid")
	r.Contains(err.Error(), "checkGroupUid")

	// Both targets.
	_, err = svc.CreateResource(ctx, org.Slug, page.UID, section.UID, CreateResourceRequest{
		CheckUID: check.UID, CheckGroupUID: group.UID,
	})
	r.ErrorIs(err, ErrResourceTargetInvalid)

	// Check only.
	checkRes, err := svc.CreateResource(ctx, org.Slug, page.UID, section.UID,
		CreateResourceRequest{CheckUID: check.UID})
	r.NoError(err)
	r.NotNil(checkRes.CheckUID)
	r.Equal(check.UID, *checkRes.CheckUID)
	r.Nil(checkRes.CheckGroupUID)

	// Group only — accepted by slug as well as UID.
	groupRes, err := svc.CreateResource(ctx, org.Slug, page.UID, section.UID,
		CreateResourceRequest{CheckGroupUID: "web-frontend"})
	r.NoError(err)
	r.Nil(groupRes.CheckUID)
	r.NotNil(groupRes.CheckGroupUID)
	r.Equal(group.UID, *groupRes.CheckGroupUID)
}

// TestCreateResource_UnknownGroupIsNotFound pins that a group identifier that
// resolves to nothing is a NOT FOUND, not a target-validation error.
func TestCreateResource_UnknownGroupIsNotFound(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)
	page, section := seedPageWithSection(ctx, t, svc, org.Slug, "")

	_, err := svc.CreateResource(ctx, org.Slug, page.UID, section.UID,
		CreateResourceRequest{CheckGroupUID: "no-such-group"})
	r.ErrorIs(err, ErrCheckGroupNotFound)
}

// TestUpdateResource_SwitchesTargetKind pins that a PATCH can flip an existing
// resource between the two target kinds — and that switching always clears the
// other column, so the XOR invariant survives the round trip.
func TestUpdateResource_SwitchesTargetKind(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)
	page, section := seedPageWithSection(ctx, t, svc, org.Slug, "")

	check := models.NewCheck(org.UID, "api", "http")
	r.NoError(svc.db.CreateCheck(ctx, check))

	group := models.NewCheckGroup(org.UID, "Web frontend", "web-frontend")
	r.NoError(svc.db.CreateCheckGroup(ctx, group))

	resource, err := svc.CreateResource(ctx, org.Slug, page.UID, section.UID,
		CreateResourceRequest{CheckUID: check.UID})
	r.NoError(err)

	// check -> group
	updated, err := svc.UpdateResource(ctx, org.Slug, page.UID, section.UID, resource.UID,
		UpdateResourceRequest{CheckGroupUID: strPtr(group.UID)})
	r.NoError(err)
	r.Nil(updated.CheckUID, "switching to a group must clear check_uid")
	r.NotNil(updated.CheckGroupUID)
	r.Equal(group.UID, *updated.CheckGroupUID)

	// group -> check
	updated, err = svc.UpdateResource(ctx, org.Slug, page.UID, section.UID, resource.UID,
		UpdateResourceRequest{CheckUID: strPtr(check.UID)})
	r.NoError(err)
	r.Nil(updated.CheckGroupUID, "switching back to a check must clear check_group_uid")
	r.NotNil(updated.CheckUID)
	r.Equal(check.UID, *updated.CheckUID)

	// Both at once is the same validation error as on create.
	_, err = svc.UpdateResource(ctx, org.Slug, page.UID, section.UID, resource.UID,
		UpdateResourceRequest{CheckUID: strPtr(check.UID), CheckGroupUID: strPtr(group.UID)})
	r.ErrorIs(err, ErrResourceTargetInvalid)

	// Neither leaves the target untouched (only the other fields are written).
	newName := "Public API"
	updated, err = svc.UpdateResource(ctx, org.Slug, page.UID, section.UID, resource.UID,
		UpdateResourceRequest{PublicName: &newName})
	r.NoError(err)
	r.NotNil(updated.CheckUID)
	r.Equal(check.UID, *updated.CheckUID)
	r.Nil(updated.CheckGroupUID)
}

// TestViewStatusPage_GroupRollupStatus drives the public view for a group
// resource across mixed member states and pins the resulting single public
// status. Disabled members are excluded — the same member predicate the shared
// rollup (spec 2026-08-01-01) uses.
func TestViewStatusPage_GroupRollupStatus(t *testing.T) {
	t.Parallel()

	type member struct {
		status  models.CheckStatus
		enabled bool
	}

	testCases := []struct {
		name    string
		members []member
		want    string
	}{
		{
			name:    "all up",
			members: []member{{models.CheckStatusUp, true}, {models.CheckStatusUp, true}},
			want:    "up",
		},
		{
			name:    "all down",
			members: []member{{models.CheckStatusDown, true}, {models.CheckStatusDown, true}},
			want:    "down",
		},
		{
			name:    "partial failure is degraded",
			members: []member{{models.CheckStatusUp, true}, {models.CheckStatusDown, true}},
			want:    "degraded",
		},
		{
			name:    "warning member paints amber not red",
			members: []member{{models.CheckStatusUp, true}, {models.CheckStatusWarning, true}},
			want:    "warning",
		},
		{
			name:    "validating reads up publicly",
			members: []member{{models.CheckStatusValidating, true}},
			want:    "up",
		},
		{
			name:    "no members at all",
			members: nil,
			want:    "created",
		},
		{
			name: "disabled down member is excluded",
			members: []member{
				{models.CheckStatusUp, true},
				{models.CheckStatusDown, false},
			},
			want: "up",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx, svc, org := setupStatusPagesTest(t)
			page, section := seedPageWithSection(ctx, t, svc, org.Slug, "")

			members := make([]struct {
				status  models.CheckStatus
				enabled bool
			}, len(tc.members))
			for i, m := range tc.members {
				members[i].status = m.status
				members[i].enabled = m.enabled
			}

			group := seedGroup(ctx, t, svc, org.UID, "web-frontend", members)

			_, err := svc.CreateResource(ctx, org.Slug, page.UID, section.UID,
				CreateResourceRequest{CheckGroupUID: group.UID})
			r.NoError(err)

			view, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug)
			r.NoError(err)
			r.Len(view.Sections, 1)
			r.Len(view.Sections[0].Resources, 1)

			info := view.Sections[0].Resources[0].Check
			r.NotNil(info, "a group resource must carry a live info block")
			r.Equal(tc.want, info.Status)
		})
	}
}

// TestViewStatusPage_GroupHidesTopology is the security-shaped assertion: the
// public payload for a group resource must expose the GROUP's name and nothing
// about its members — no member names, no member count, not even a check type
// that would betray the aggregation.
func TestViewStatusPage_GroupHidesTopology(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)
	page, section := seedPageWithSection(ctx, t, svc, org.Slug, "")

	group := models.NewCheckGroup(org.UID, "Web frontend", "web-frontend")
	r.NoError(svc.db.CreateCheckGroup(ctx, group))

	// Two members with very identifiable names/types.
	for _, spec := range []struct{ slug, typ string }{{"internal-rdp-probe", "tcp"}, {"internal-tls-probe", "ssl"}} {
		check := models.NewCheck(org.UID, spec.slug, spec.typ)
		name := spec.slug
		check.Name = &name
		check.CheckGroupUID = &group.UID
		check.Status = models.CheckStatusUp
		r.NoError(svc.db.CreateCheck(ctx, check))
	}

	_, err := svc.CreateResource(ctx, org.Slug, page.UID, section.UID,
		CreateResourceRequest{CheckGroupUID: group.UID})
	r.NoError(err)

	view, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug)
	r.NoError(err)

	resource := view.Sections[0].Resources[0]
	r.Nil(resource.CheckUID, "a group resource carries no checkUid")
	r.NotNil(resource.CheckGroupUID)

	info := resource.Check
	r.NotNil(info)
	r.NotNil(info.Name)
	r.Equal("Web frontend", *info.Name, "the GROUP name is published, never a member's")
	r.Empty(info.Type, "no check type is published for a group — it would hint at the aggregation")

	// Serialize the whole public response and assert no member identifier leaks
	// anywhere in it (name, slug, type or UID).
	body := mustMarshalJSON(t, view)
	r.NotContains(body, "internal-rdp-probe")
	r.NotContains(body, "internal-tls-probe")
}

// TestViewStatusPage_GroupWeightedAvailability pins the aggregation formula:
// per bucket, sum(successful) / sum(total) across members — NOT the mean of
// per-member percentages — and a member with no rows in a bucket contributes
// nothing to it rather than counting as zero.
func TestViewStatusPage_GroupWeightedAvailability(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)
	page, section := seedPageWithSection(ctx, t, svc, org.Slug, "7d")

	group := models.NewCheckGroup(org.UID, "Web frontend", "web-frontend")
	r.NoError(svc.db.CreateCheckGroup(ctx, group))

	sparse := models.NewCheck(org.UID, "sparse", "http")
	sparse.CheckGroupUID = &group.UID
	sparse.Status = models.CheckStatusUp
	r.NoError(svc.db.CreateCheck(ctx, sparse))

	dense := models.NewCheck(org.UID, "dense", "http")
	dense.CheckGroupUID = &group.UID
	dense.Status = models.CheckStatusUp
	r.NoError(svc.db.CreateCheck(ctx, dense))

	_, err := svc.CreateResource(ctx, org.Slug, page.UID, section.UID,
		CreateResourceRequest{CheckGroupUID: group.UID})
	r.NoError(err)

	// Raw rows older than the raw-retention window cannot exist in production
	// (the aggregation job rolls them up and deletes them in one transaction),
	// and uptimebar clamps its raw-tier query accordingly. This fixture's raw
	// rows are up to two days old, so the page must be configured to keep raw
	// that long for them to be a realistic dataset.
	svc.cfg.Aggregation.RetentionRaw = 7 * 24

	now := time.Now().UTC()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	yesterday := todayStart.AddDate(0, 0, -1)
	dayBefore := todayStart.AddDate(0, 0, -2)

	seed := func(checkUID string, day time.Time, up, down int) {
		for i := range up {
			res := models.NewResult(org.UID, checkUID, models.ResultStatusUp, 42)
			res.PeriodStart = day.Add(time.Duration(i) * time.Minute)
			r.NoError(svc.db.CreateResult(ctx, res))
		}

		for i := range down {
			res := models.NewResult(org.UID, checkUID, models.ResultStatusDown, 0)
			res.PeriodStart = day.Add(time.Duration(up+i) * time.Minute)
			r.NoError(svc.db.CreateResult(ctx, res))
		}
	}

	// YESTERDAY — asymmetric density:
	//   sparse: 9 up / 1 down  -> 90% over 10 samples
	//   dense: 90 up / 0 down  -> 100% over 90 samples
	// weighted: (9 + 90) / (10 + 90) = 99%.   mean-of-percentages would be 95%.
	seed(sparse.UID, yesterday, 9, 1)
	seed(dense.UID, yesterday, 90, 0)

	// DAY BEFORE — only `sparse` reported at all. A silent member contributes
	// nothing, so the bucket is 100%, not 50%.
	seed(sparse.UID, dayBefore, 2, 0)

	view, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug)
	r.NoError(err)

	avail := view.Sections[0].Resources[0].Availability
	r.NotNil(avail)
	r.Len(avail.DailyAvailability, 7)

	byDate := make(map[string]AvailabilityPoint, len(avail.DailyAvailability))
	for _, point := range avail.DailyAvailability {
		byDate[point.Date] = point
	}

	yesterdayPoint, ok := byDate[yesterday.Format("2006-01-02")]
	r.True(ok)
	r.InDelta(99.0, yesterdayPoint.AvailabilityPct, 0.001,
		"weighted average across members, not the mean of their percentages")

	dayBeforePoint, ok := byDate[dayBefore.Format("2006-01-02")]
	r.True(ok)
	r.InDelta(100.0, dayBeforePoint.AvailabilityPct, 0.001,
		"a member with no data in the bucket contributes nothing, not zero")

	// The overall percentage weights the buckets the same way:
	// (9+90+2) / (10+90+2) = 101/102.
	r.NotNil(avail.OverallAvailabilityPct)
	r.InDelta(100.0*101.0/102.0, *avail.OverallAvailabilityPct, 0.01)

	// No per-member response-time series is published for a group.
	r.Empty(avail.ResponseTimeSeries)
}

// TestViewStatusPage_GroupMaintenance pins that a group component reads "in
// maintenance" for a window targeting the group itself AND for one targeting
// only one of its member checks.
func TestViewStatusPage_GroupMaintenance(t *testing.T) {
	t.Parallel()

	now := time.Now()

	testCases := []struct {
		name string
		// target picks what the window is attached to.
		target string // "none" | "group" | "member"
		want   bool
	}{
		{name: "no window", target: "none", want: false},
		{name: "group-targeted window", target: "group", want: true},
		{name: "member-targeted window", target: "member", want: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx, svc, org := setupStatusPagesTest(t)
			page, section := seedPageWithSection(ctx, t, svc, org.Slug, "")

			group := models.NewCheckGroup(org.UID, "Web frontend", "web-frontend")
			r.NoError(svc.db.CreateCheckGroup(ctx, group))

			member := models.NewCheck(org.UID, "member", "http")
			member.CheckGroupUID = &group.UID
			member.Status = models.CheckStatusUp
			r.NoError(svc.db.CreateCheck(ctx, member))

			if tc.target != "none" {
				win := models.NewMaintenanceWindow(org.UID, "Planned",
					now.Add(-time.Hour), now.Add(time.Hour))
				r.NoError(svc.db.CreateMaintenanceWindow(ctx, win))

				if tc.target == "group" {
					r.NoError(svc.db.SetMaintenanceWindowChecks(ctx, win.UID, nil, []string{group.UID}))
				} else {
					r.NoError(svc.db.SetMaintenanceWindowChecks(ctx, win.UID, []string{member.UID}, nil))
				}
			}

			_, err := svc.CreateResource(ctx, org.Slug, page.UID, section.UID,
				CreateResourceRequest{CheckGroupUID: group.UID})
			r.NoError(err)

			view, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug)
			r.NoError(err)

			info := view.Sections[0].Resources[0].Check
			r.NotNil(info)
			r.Equal(tc.want, info.InMaintenance)
		})
	}
}

// TestViewStatusPage_DeletedGroupMatchesDeletedCheck pins the orphan policy:
// deleting the target of a resource is a SOFT delete for both kinds, so the
// resource row survives and simply renders with no live info block. A group
// resource must behave exactly like a check resource here — no new policy was
// invented for groups.
func TestViewStatusPage_DeletedGroupMatchesDeletedCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)
	page, section := seedPageWithSection(ctx, t, svc, org.Slug, "")

	// A lone check resource whose check gets deleted.
	lone := models.NewCheck(org.UID, "lone", "http")
	lone.Status = models.CheckStatusUp
	r.NoError(svc.db.CreateCheck(ctx, lone))

	// A group resource whose group gets deleted.
	group := seedGroup(ctx, t, svc, org.UID, "web-frontend", []struct {
		status  models.CheckStatus
		enabled bool
	}{{models.CheckStatusUp, true}})

	_, err := svc.CreateResource(ctx, org.Slug, page.UID, section.UID,
		CreateResourceRequest{CheckUID: lone.UID})
	r.NoError(err)
	_, err = svc.CreateResource(ctx, org.Slug, page.UID, section.UID,
		CreateResourceRequest{CheckGroupUID: group.UID})
	r.NoError(err)

	r.NoError(svc.db.DeleteCheck(ctx, lone.UID))
	r.NoError(svc.db.DeleteCheckGroup(ctx, group.UID))

	view, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug)
	r.NoError(err)
	r.Len(view.Sections[0].Resources, 2, "both resources survive their target's soft delete")

	for _, resource := range view.Sections[0].Resources {
		r.Nil(resource.Check, "a soft-deleted target leaves the resource with no live info")
	}
}

// TestMergeBuckets_SumsAcrossMembers is the unit-level pin on the merge helper
// the group availability path is built on: stats are summed, so the derived
// percentage is the weighted average and an absent member is simply absent.
func TestMergeBuckets_SumsAcrossMembers(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	bucket := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	other := bucket.AddDate(0, 0, -1)

	buckets := map[string]map[time.Time]uptimebar.BucketStats{
		"a": {bucket: {Up: 9, Total: 10, DurCnt: 10, DurSum: 100}, other: {Up: 2, Total: 2}},
		"b": {bucket: {Up: 90, Total: 90, DurCnt: 90, DurSum: 900}},
	}

	merged := mergeBuckets(buckets, []string{"a", "b"})

	r.Equal(99, merged[bucket].Up)
	r.Equal(100, merged[bucket].Total)
	r.Equal(100, merged[bucket].DurCnt)
	r.InDelta(1000.0, merged[bucket].DurSum, 0.001)

	pct, hasData := merged[bucket].AvailabilityPct()
	r.True(hasData)
	r.InDelta(99.0, pct, 0.001)

	// Only "a" reported in the older bucket — it contributes alone, at 100%.
	pct, hasData = merged[other].AvailabilityPct()
	r.True(hasData)
	r.InDelta(100.0, pct, 0.001)

	// A single member short-circuits to that member's own series unchanged.
	r.Equal(buckets["b"], mergeBuckets(buckets, []string{"b"}))
}
