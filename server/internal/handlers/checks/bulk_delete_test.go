package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

func bulkDeleteSkips(resp *checks.BulkDeleteChecksResponse) map[string]string {
	codes := map[string]string{}
	for _, skip := range resp.Skipped {
		codes[skip.CheckUID] = skip.Code
	}

	return codes
}

// TestBulkDeleteMixed: eligible checks are deleted, every other kind is skipped
// with its code, and a dry run writes nothing (spec 2026-11-21-03).
func TestBulkDeleteMixed(t *testing.T) {
	t.Parallel()

	svc, dbSvc, org := setupTunnelChecksService(t)
	runBulkDeleteMixed(t, svc, dbSvc, org)
}

func runBulkDeleteMixed(t *testing.T, svc *checks.Service, dbSvc db.Service, org *models.Organization) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	plain1, err := svc.CreateCheck(ctx, org.Slug, httpCheckReq())
	r.NoError(err)
	plain2, err := svc.CreateCheck(ctx, org.Slug, httpCheckReq())
	r.NoError(err)
	kept, err := svc.CreateCheck(ctx, org.Slug, httpCheckReq())
	r.NoError(err)

	managedReq := httpCheckReq()
	managedReq.Labels = map[string]string{checks.ManagedLabelKey: "manifest"}
	managed, err := svc.CreateCheck(ctx, org.Slug, managedReq)
	r.NoError(err)

	internal, err := svc.CreateCheck(ctx, org.Slug, httpCheckReq())
	r.NoError(err)
	_, err = dbSvc.DB().NewUpdate().Model((*models.Check)(nil)).
		Set("internal = ?", true).Where("uid = ?", internal.UID).Exec(ctx)
	r.NoError(err)

	bastion := createBastion(t, svc, ctx, org, "bastion")
	_, err = svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "dep", Slug: "dep", Type: "http",
		Config: map[string]any{
			"url":                              "http://internal.private/health",
			checkerdef.TunnelCheckUIDConfigKey: bastion.UID,
		},
	})
	r.NoError(err)

	otherOrg := models.NewOrganization("other-org", "Other")
	r.NoError(dbSvc.CreateOrganization(ctx, otherOrg))
	foreign, err := svc.CreateCheck(ctx, otherOrg.Slug, httpCheckReq())
	r.NoError(err)

	uids := []string{plain1.UID, plain2.UID, managed.UID, internal.UID, bastion.UID, foreign.UID, missingCheckUID}

	dry, err := svc.BulkDeleteChecks(ctx, org.Slug, &checks.BulkDeleteChecksRequest{CheckUIDs: uids, DryRun: true})
	r.NoError(err)
	r.True(dry.DryRun)
	r.ElementsMatch([]string{plain1.UID, plain2.UID}, dry.Deleted)

	_, err = svc.GetCheck(ctx, org.Slug, plain1.UID, checks.GetCheckOptions{})
	r.NoError(err, "a dry run deletes nothing")

	resp, err := svc.BulkDeleteChecks(ctx, org.Slug, &checks.BulkDeleteChecksRequest{CheckUIDs: uids})
	r.NoError(err)
	r.ElementsMatch([]string{plain1.UID, plain2.UID}, resp.Deleted)

	codes := bulkDeleteSkips(resp)
	r.Equal(checks.BulkDeleteSkipManaged, codes[managed.UID])
	r.Equal(checks.BulkDeleteSkipInternal, codes[internal.UID])
	r.Equal(checks.BulkDeleteSkipTunnelInUse, codes[bastion.UID])
	r.Equal(checks.BulkDeleteSkipNotFound, codes[foreign.UID])
	r.Equal(checks.BulkDeleteSkipNotFound, codes[missingCheckUID])

	_, err = svc.GetCheck(ctx, org.Slug, plain1.UID, checks.GetCheckOptions{})
	r.ErrorIs(err, checks.ErrCheckNotFound)
	_, err = svc.GetCheck(ctx, org.Slug, kept.UID, checks.GetCheckOptions{})
	r.NoError(err, "an unlisted check remains")
	_, err = svc.GetCheck(ctx, otherOrg.Slug, foreign.UID, checks.GetCheckOptions{})
	r.NoError(err, "another org's check remains")
}

// TestBulkDeleteDemoOwnChecksOnly: a demo session deletes only its own checks.
func TestBulkDeleteDemoOwnChecksOnly(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	svc, _, org := setupQuotaService(t, 100)

	mine, err := svc.CreateCheck(demoCtx("demo-user"), org.Slug, httpCheckReq())
	r.NoError(err)
	seeded, err := svc.CreateCheck(t.Context(), org.Slug, httpCheckReq())
	r.NoError(err)

	resp, err := svc.BulkDeleteChecks(demoCtx("demo-user"), org.Slug,
		&checks.BulkDeleteChecksRequest{CheckUIDs: []string{mine.UID, seeded.UID}})
	r.NoError(err)
	r.Equal([]string{mine.UID}, resp.Deleted)
	r.Equal(checks.BulkDeleteSkipReadOnly, bulkDeleteSkips(resp)[seeded.UID])
}

// TestBulkDeleteRejectsBadRequests: empty and oversized lists are 400-class.
func TestBulkDeleteRejectsBadRequests(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	svc, _, org := setupTunnelChecksService(t)

	_, err := svc.BulkDeleteChecks(t.Context(), org.Slug, &checks.BulkDeleteChecksRequest{})
	r.Error(err)
	r.True(checks.IsBulkUpdateRequestError(err))

	tooMany := make([]string, checks.MaxBulkUpdateChecks+1)
	for i := range tooMany {
		tooMany[i] = missingCheckUID
	}

	_, err = svc.BulkDeleteChecks(t.Context(), org.Slug, &checks.BulkDeleteChecksRequest{CheckUIDs: tooMany})
	r.Error(err)
	r.True(checks.IsBulkUpdateRequestError(err))
}
