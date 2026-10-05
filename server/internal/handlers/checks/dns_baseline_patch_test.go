package checks_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

func TestDNSBaselinePatch(t *testing.T) {
	t.Parallel()

	setup := func(t *testing.T) (*checks.Service, string, string, func() models.JSONMap, db.Service) {
		t.Helper()

		r := require.New(t)
		ctx := t.Context()
		svc, dbSvc, _, org := setupPlaintextChecksService(t)

		created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
			Name: "dns-ns",
			Slug: "dns-ns",
			Type: "dns",
			Config: map[string]any{
				"host": "acme.com", "record_type": "NS", "detect_changes": true,
			},
		})
		r.NoError(err)

		written, err := dbSvc.CaptureCheckConfigBaseline(ctx, created.UID, "eu-west", []string{"ns1.acme.com"})
		r.NoError(err)
		r.True(written)

		stored := func() models.JSONMap {
			row, getErr := dbSvc.GetCheck(ctx, org.UID, created.UID)
			r.NoError(getErr)

			return row.Config
		}

		return svc, org.Slug, created.UID, stored, dbSvc
	}

	t.Run("PATCH without baseline keeps it", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		svc, orgSlug, uid, stored, _ := setup(t)

		patch := map[string]any{
			"host": "acme.com", "record_type": "NS", "detect_changes": true, "on_change": "warning",
		}
		updated, err := svc.UpdateCheck(t.Context(), orgSlug, uid, &checks.UpdateCheckRequest{Config: &patch})
		r.NoError(err)
		r.Equal("warning", updated.Config["on_change"])

		baseline, ok := stored()["baseline"].(map[string]any)
		r.True(ok, "the baseline must survive a PATCH that omits it")
		r.Equal([]any{"ns1.acme.com"}, baseline["eu-west"])
	})

	t.Run("PATCH with an empty baseline clears it and runs the check", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		svc, orgSlug, uid, stored, dbSvc := setup(t)

		later := time.Now().Add(time.Hour)
		_, err := dbSvc.DB().NewUpdate().Model((*models.CheckJob)(nil)).
			Set("scheduled_at = ?", later).Where("check_uid = ?", uid).Exec(t.Context())
		r.NoError(err)

		patch := map[string]any{
			"host": "acme.com", "record_type": "NS", "detect_changes": true, "baseline": map[string]any{},
		}
		_, err = svc.UpdateCheck(t.Context(), orgSlug, uid, &checks.UpdateCheckRequest{Config: &patch})
		r.NoError(err)

		baseline, _ := stored()["baseline"].(map[string]any)
		r.Empty(baseline, "accepting the current records resets every region")

		jobs, err := dbSvc.ListCheckJobsByCheckUID(t.Context(), uid)
		r.NoError(err)
		r.NotEmpty(jobs)

		for _, job := range jobs {
			r.NotNil(job.ScheduledAt)
			r.True(job.ScheduledAt.Before(time.Now().Add(time.Second)), "the reset makes every region run now")
		}
	})

	t.Run("turning detection off drops the baseline", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		svc, orgSlug, uid, stored, _ := setup(t)

		patch := map[string]any{"host": "acme.com", "record_type": "NS"}
		_, err := svc.UpdateCheck(t.Context(), orgSlug, uid, &checks.UpdateCheckRequest{Config: &patch})
		r.NoError(err)
		r.NotContains(stored(), "baseline")
	})

	t.Run("baseline without detection is refused", func(t *testing.T) {
		t.Parallel()

		svc, orgSlug, uid, _, _ := setup(t)

		patch := map[string]any{
			"host": "acme.com", "record_type": "NS",
			"baseline": map[string]any{"eu-west": []any{"ns1.acme.com"}},
		}
		_, err := svc.UpdateCheck(t.Context(), orgSlug, uid, &checks.UpdateCheckRequest{Config: &patch})
		require.Error(t, err)
	})
}

// TestDNSBaselineApplyIsUnchanged pins the config-as-code side: re-applying a
// manifest exported before the capture plans no change and keeps the baseline.
func TestDNSBaselineApplyIsUnchanged(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	rig := newRoundTripRig(t)

	doc := importOne(rig.org.Slug, checks.ExportCheck{
		Name: "NS", Slug: "dns-ns", Type: "dns", Enabled: true,
		Config: map[string]any{"host": "acme.com", "record_type": "NS", "detect_changes": true},
	})
	_, err := rig.svc.ApplyChecks(t.Context(), rig.org.Slug, doc, checks.ApplyOptions{})
	r.NoError(err)

	stored, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, "dns-ns")
	r.NoError(err)

	written, err := rig.dbSvc.CaptureCheckConfigBaseline(t.Context(), stored.UID, "eu-west", []string{"ns1.acme.com"})
	r.NoError(err)
	r.True(written)

	dry, err := rig.svc.ApplyChecks(t.Context(), rig.org.Slug, doc, checks.ApplyOptions{DryRun: true})
	r.NoError(err)
	r.Equalf(1, dry.Unchanged, "an omitted baseline is no drift: %+v", dry.Plan)

	_, err = rig.svc.ApplyChecks(t.Context(), rig.org.Slug, doc, checks.ApplyOptions{})
	r.NoError(err)

	after, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, "dns-ns")
	r.NoError(err)
	r.Contains(after.Config, "baseline")
}
