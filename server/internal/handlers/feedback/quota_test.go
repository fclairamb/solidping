package feedback

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
)

// The local filestorage backend is registered explicitly by server bootstrap
// (internal/app/server.go), not via package init — these tests exercise the
// full files.Service.CreateFile path, so they need the same registration.
//
//nolint:gochecknoinits // test-only bootstrap, mirrors server.go's own call
func init() {
	localfs.Register()
}

// newFeedbackQuotaConfig builds a minimal config for the storage-quota
// tests: a local filestorage backend rooted at a fresh temp dir, and the
// given per-org feedback quota.
func newFeedbackQuotaConfig(t *testing.T, quotaBytes int64) *config.Config {
	t.Helper()

	cfg := &config.Config{}
	cfg.App.FeedbackMaxStorageBytes = quotaBytes
	cfg.FileStorage.Type = "local"
	cfg.FileStorage.LocalRoot = t.TempDir()

	return cfg
}

// assertStorageQuotaEnforced is run against both database engines (spec
// 2026-09-25-26 Tests: "seed feedback files over the cap → next report 413,
// no row written (both DBs)"). It seeds one feedback file whose size alone
// equals the quota, then asserts a further screenshot is rejected with
// ErrStorageQuotaExceeded and persists no new row, and that raising the quota
// lets the same request through.
func assertStorageQuotaEnforced(ctx context.Context, t *testing.T, dbSvc db.Service, orgUID, orgSlug string) {
	t.Helper()
	r := require.New(t)

	const quota int64 = 1024

	cfg := newFeedbackQuotaConfig(t, quota)

	// Seed a "live" feedback file directly (bypassing the service) whose
	// file_uri carries the reports group segment SumFileSizeByGroup matches
	// on, sized to exactly the quota so any further screenshot bytes exceed it.
	seeded := models.NewFile(orgUID, "seed.png", "image/png",
		fmt.Sprintf("file://%s/%s/seed", orgUID, filestorage.GroupTypeReports),
		quota, nil)
	r.NoError(dbSvc.CreateFile(ctx, seeded))

	filesSvc := files.NewService(dbSvc, cfg)
	svc := NewService(dbSvc, filesSvc, cfg, nil)

	_, err := svc.SubmitReport(ctx, &SubmitReportRequest{
		URL:            "https://example.com/page",
		OrgSlug:        orgSlug,
		Screenshot:     strings.NewReader("x"),
		ScreenshotSize: 1,
		ScreenshotName: "s.png",
		ScreenshotMIME: "image/png",
	})
	r.ErrorIs(err, ErrStorageQuotaExceeded)

	total, sumErr := dbSvc.SumFileSizeByGroup(ctx, orgUID, string(filestorage.GroupTypeReports))
	r.NoError(sumErr)
	r.Equal(quota, total, "a quota-rejected report must not persist a new file row")

	// Positive control: the same request, with headroom under the quota,
	// must succeed — proving the rejection above is the quota firing, not a
	// broken store.
	roomy := *cfg
	roomy.App.FeedbackMaxStorageBytes = quota + 1024
	svcRoomy := NewService(dbSvc, filesSvc, &roomy, nil)

	resp, err := svcRoomy.SubmitReport(ctx, &SubmitReportRequest{
		URL:            "https://example.com/page",
		OrgSlug:        orgSlug,
		Screenshot:     strings.NewReader("y"),
		ScreenshotSize: 1,
		ScreenshotName: "s2.png",
		ScreenshotMIME: "image/png",
	})
	r.NoError(err)
	r.NotEmpty(resp.UID)

	total, sumErr = dbSvc.SumFileSizeByGroup(ctx, orgUID, string(filestorage.GroupTypeReports))
	r.NoError(sumErr)
	r.Equal(quota+1, total, "the accepted report must persist its row")
}

// TestSubmitReport_StorageQuota_SQLite is the SQLite half of the pair; see
// service_quota_postgres_test.go for the Postgres half.
func TestSubmitReport_StorageQuota_SQLite(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("fb-quota-sqlite", "Feedback Quota SQLite")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	assertStorageQuotaEnforced(ctx, t, dbSvc, org.UID, org.Slug)
}

// TestSubmitReport_DisabledQuota_DoesNotBlock proves 0 (and negative) disables
// the quota entirely, per config.AppConfig.FeedbackMaxStorageBytes's doc
// comment — a deployment that never sets the parameter must not be silently
// throttled.
func TestSubmitReport_DisabledQuota_DoesNotBlock(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("fb-quota-disabled", "Feedback Quota Disabled")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	cfg := newFeedbackQuotaConfig(t, 0)

	// Seed well beyond what any real quota would allow.
	seeded := models.NewFile(org.UID, "seed.png", "image/png",
		fmt.Sprintf("file://%s/%s/seed", org.UID, filestorage.GroupTypeReports),
		10*1024*1024*1024, nil)
	r.NoError(dbSvc.CreateFile(ctx, seeded))

	filesSvc := files.NewService(dbSvc, cfg)
	svc := NewService(dbSvc, filesSvc, cfg, nil)

	resp, err := svc.SubmitReport(ctx, &SubmitReportRequest{
		URL:            "https://example.com/page",
		OrgSlug:        org.Slug,
		Screenshot:     strings.NewReader("x"),
		ScreenshotSize: 1,
		ScreenshotName: "s.png",
		ScreenshotMIME: "image/png",
	})
	r.NoError(err)
	r.NotEmpty(resp.UID)
}
