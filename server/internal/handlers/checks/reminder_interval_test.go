package checks_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

func reminderSeconds(v int) *int { return &v }

// A PATCH without the field leaves the interval alone, null puts the check
// back on the org default, and an out-of-range value is refused naming the
// field (spec 2026-10-12-16).
func TestReminderIntervalPatchSemantics(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "api", Slug: "api", Type: "http",
		Config:                  map[string]any{"url": "https://acme.com/api"},
		Regions:                 []string{"default"},
		ReminderIntervalSeconds: reminderSeconds(1800),
	})
	r.NoError(err)
	r.NotNil(created.ReminderIntervalSeconds)
	r.Equal(1800, *created.ReminderIntervalSeconds)

	// A PATCH that does not mention the field leaves it.
	var untouched checks.UpdateCheckRequest
	r.NoError(json.Unmarshal([]byte(`{"description":"edited"}`), &untouched))
	r.False(untouched.ReminderIntervalSeconds.Set)

	updated, err := svc.UpdateCheck(ctx, org.Slug, created.UID, &untouched)
	r.NoError(err)
	r.NotNil(updated.ReminderIntervalSeconds)
	r.Equal(1800, *updated.ReminderIntervalSeconds)

	// Out of range: refused, naming the field, and nothing written.
	var invalid checks.UpdateCheckRequest
	r.NoError(json.Unmarshal([]byte(`{"reminderIntervalSeconds":899}`), &invalid))
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &invalid)
	r.ErrorContains(err, "reminderIntervalSeconds")

	// 0 turns it off.
	var off checks.UpdateCheckRequest
	r.NoError(json.Unmarshal([]byte(`{"reminderIntervalSeconds":0}`), &off))
	updated, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &off)
	r.NoError(err)
	r.NotNil(updated.ReminderIntervalSeconds)
	r.Equal(0, *updated.ReminderIntervalSeconds)

	// null goes back to the org default.
	var inherit checks.UpdateCheckRequest
	r.NoError(json.Unmarshal([]byte(`{"reminderIntervalSeconds":null}`), &inherit))
	r.True(inherit.ReminderIntervalSeconds.Set)
	updated, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &inherit)
	r.NoError(err)
	r.Nil(updated.ReminderIntervalSeconds)

	stored, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "api")
	r.NoError(err)
	r.Nil(stored.ReminderIntervalSeconds)

	// The response always carries the field, null included.
	raw, err := json.Marshal(updated)
	r.NoError(err)
	r.Contains(string(raw), `"reminderIntervalSeconds":null`)
}

func TestReminderIntervalSurvivesCloneAndExportImport(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	mk := func(slug string, interval *int) {
		_, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
			Name: slug, Slug: slug, Type: "http",
			Config:                  map[string]any{"url": "https://acme.com/" + slug},
			Regions:                 []string{"default"},
			ReminderIntervalSeconds: interval,
		})
		r.NoError(err)
	}

	mk("hourly", reminderSeconds(3600))
	mk("silent", reminderSeconds(0))
	mk("inherits", nil)

	source, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "hourly")
	r.NoError(err)

	clone, err := svc.CloneCheck(ctx, org.Slug, source.UID, &checks.CloneCheckRequest{})
	r.NoError(err)
	r.NotNil(clone.ReminderIntervalSeconds, "a clone keeps the source's interval")
	r.Equal(3600, *clone.ReminderIntervalSeconds)

	exported, err := svc.ExportChecks(ctx, org.Slug, checks.ListChecksOptions{})
	r.NoError(err)

	body, err := checks.MarshalExportDocument(exported)
	r.NoError(err)
	r.Contains(string(body), `"reminderIntervalSeconds": 3600`)
	r.Contains(string(body), `"reminderIntervalSeconds": 0`, "an explicit off must travel")

	cleanOrg := makeCleanOrg(ctx, t, dbSvc, "reminder-clean-org")

	var reimport checks.ExportDocument
	r.NoError(json.Unmarshal(body, &reimport))

	result, err := svc.ImportChecks(ctx, cleanOrg.Slug, &reimport, false)
	r.NoError(err)
	r.Empty(result.Errors)

	for slug, want := range map[string]*int{
		"hourly": reminderSeconds(3600), "silent": reminderSeconds(0), "inherits": nil,
	} {
		stored, getErr := dbSvc.GetCheckByUidOrSlug(ctx, cleanOrg.UID, slug)
		r.NoError(getErr)
		r.Equal(want, stored.ReminderIntervalSeconds, slug)
	}

	// Re-importing a document without the field moves an override back to
	// inherit.
	doc := &checks.ExportDocument{
		Version: 2,
		Checks: []checks.ExportCheck{{
			Name: "hourly", Slug: "hourly", Type: "http",
			Config:  map[string]any{"url": "https://acme.com/hourly"},
			Regions: []string{"default"},
			Enabled: true,
		}},
	}

	result, err = svc.ImportChecks(ctx, cleanOrg.Slug, doc, false)
	r.NoError(err)
	r.Empty(result.Errors)

	moved, err := dbSvc.GetCheckByUidOrSlug(ctx, cleanOrg.UID, "hourly")
	r.NoError(err)
	r.Nil(moved.ReminderIntervalSeconds)
}
