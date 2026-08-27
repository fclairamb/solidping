package integration

import (
	"bytes"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// ptrOf returns a pointer to v — a tiny helper for building optional request bodies.
func ptrOf[T any](v T) *T { return &v }

// TestCLICoverage_Entitlements exercises the getEntitlements / setEntitlements /
// patchEntitlements / listEntitlementsAudits generated-client operations.
func TestCLICoverage_Entitlements(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()
	apiClient := cliCovAuthedClient(ctx, t, ts)

	// GET the defaults for a fresh org (self-hosted default caps SSO seats).
	getResp, err := apiClient.GetEntitlementsWithResponse(ctx, TestOrgSlug, &client.GetEntitlementsParams{})
	r.NoError(err)
	r.Equal(200, getResp.StatusCode())
	r.NotNil(getResp.JSON200)
	r.NotNil(getResp.JSON200.Limits.MaxUsers)
	r.Equal(30, *getResp.JSON200.Limits.MaxUsers)

	// SET (PUT) replaces the row with explicit limits + display identity.
	setResp, err := apiClient.SetEntitlementsWithResponse(ctx, TestOrgSlug, client.SetEntitlementsRequest{
		Limits: &client.EntitlementLimits{
			MaxChecks:          ptrOf(100),
			MaxUsers:           ptrOf(50),
			MaxChecksPerMinute: ptrOf(12),
		},
		DisplayName:  ptrOf("Team"),
		DisplayEmoji: ptrOf("🚀"),
	})
	r.NoError(err)
	r.Equal(200, setResp.StatusCode())
	r.NotNil(setResp.JSON200)
	r.NotNil(setResp.JSON200.Limits.MaxChecks)
	r.Equal(100, *setResp.JSON200.Limits.MaxChecks)
	r.NotNil(setResp.JSON200.DisplayName)
	r.Equal("Team", *setResp.JSON200.DisplayName)

	// GET after set — persisted, ORG-ADMIN-sourced, and usage included via
	// ?with=usage.
	//
	// `org-admin`, not `admin`: this write came through the org-scoped route as
	// an ordinary org admin. Since spec 2026-08-26-06 only a SUPERADMIN mints
	// `admin`, because that source now suppresses billing pushes until it is
	// explicitly released — an org admin must not be able to lock the billing
	// service out of its own org. Same paid plan weight either way.
	getResp2, err := apiClient.GetEntitlementsWithResponse(
		ctx, TestOrgSlug, &client.GetEntitlementsParams{With: ptrOf("usage")})
	r.NoError(err)
	r.Equal(200, getResp2.StatusCode())
	r.NotNil(getResp2.JSON200.Limits.MaxChecks)
	r.Equal(100, *getResp2.JSON200.Limits.MaxChecks)
	r.NotNil(getResp2.JSON200.Limits.MaxUsers)
	r.Equal(50, *getResp2.JSON200.Limits.MaxUsers)
	r.Equal("org-admin", getResp2.JSON200.Source)
	r.NotNil(getResp2.JSON200.Usage)

	// PATCH (partial) — only maxChecks changes; the rest is preserved.
	patchResp, err := apiClient.PatchEntitlementsWithResponse(ctx, TestOrgSlug, client.SetEntitlementsRequest{
		Limits: &client.EntitlementLimits{MaxChecks: ptrOf(200)},
	})
	r.NoError(err)
	r.Equal(200, patchResp.StatusCode())
	r.NotNil(patchResp.JSON200.Limits.MaxChecks)
	r.Equal(200, *patchResp.JSON200.Limits.MaxChecks)
	r.NotNil(patchResp.JSON200.Limits.MaxUsers)
	r.Equal(50, *patchResp.JSON200.Limits.MaxUsers)
	r.NotNil(patchResp.JSON200.DisplayName)
	r.Equal("Team", *patchResp.JSON200.DisplayName)

	// AUDITS — set + patch each recorded a row, attributed to the admin user.
	auditsResp, err := apiClient.ListEntitlementsAuditsWithResponse(
		ctx, TestOrgSlug, &client.ListEntitlementsAuditsParams{Limit: ptrOf(50)})
	r.NoError(err)
	r.Equal(200, auditsResp.StatusCode())
	r.NotNil(auditsResp.JSON200.Data)
	audits := *auditsResp.JSON200.Data
	r.GreaterOrEqual(len(audits), 2)
	r.Contains(audits[0].Actor, "user:")
	r.Equal("org-admin", audits[0].Source,
		"the audit records the source the server chose, not the one the body asked for")
}

// TestCLICoverage_Files exercises the listFiles / getFile / downloadFile /
// deleteFile generated-client operations against a seeded file blob.
func TestCLICoverage_Files(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	tmpDir := t.TempDir()
	ts := NewTestServerWithConfig(t, func(c *config.Config) {
		c.FileStorage.LocalRoot = tmpDir
	})
	ctx := t.Context()
	apiClient := cliCovAuthedClient(ctx, t, ts)

	// Seed a file (bytes + row) via the same storage root the server reads from.
	content := []byte("cli coverage file body")
	fileSvc := files.NewService(ts.Server.DBService(), &config.Config{
		FileStorage: config.FileStorageConfig{LocalRoot: tmpDir},
	})
	seeded, err := fileSvc.CreateFile(
		ctx, uuid.MustParse(cliCovOrgUID), filestorage.GroupTypeReports,
		"coverage.txt", "text/plain", nil, bytes.NewReader(content), int64(len(content)),
	)
	r.NoError(err)
	fileUID := uuid.MustParse(seeded.UID)

	// LIST — the seeded file shows up.
	listResp, err := apiClient.ListFilesWithResponse(ctx, TestOrgSlug, &client.ListFilesParams{})
	r.NoError(err)
	r.Equal(200, listResp.StatusCode())
	r.NotNil(listResp.JSON200)
	r.GreaterOrEqual(listResp.JSON200.Total, int64(1))
	found := false
	for i := range listResp.JSON200.Data {
		if listResp.JSON200.Data[i].Uid == fileUID {
			found = true
			r.Equal("coverage.txt", listResp.JSON200.Data[i].Name)
			r.Equal(int64(len(content)), listResp.JSON200.Data[i].Size)
		}
	}
	r.True(found, "seeded file should appear in the list")

	// GET metadata.
	getResp, err := apiClient.GetFileWithResponse(ctx, TestOrgSlug, fileUID)
	r.NoError(err)
	r.Equal(200, getResp.StatusCode())
	r.NotNil(getResp.JSON200)
	r.Equal("coverage.txt", getResp.JSON200.Name)
	r.Equal("text/plain", getResp.JSON200.MimeType)
	r.Equal(int64(len(content)), getResp.JSON200.Size)

	// DOWNLOAD content — raw bytes round-trip.
	dlResp, err := apiClient.DownloadFileWithResponse(ctx, TestOrgSlug, fileUID)
	r.NoError(err)
	r.Equal(200, dlResp.StatusCode())
	r.Equal(content, dlResp.Body)

	// REMOVE — 204, then the file is gone.
	delResp, err := apiClient.DeleteFileWithResponse(ctx, TestOrgSlug, fileUID)
	r.NoError(err)
	r.Equal(204, delResp.StatusCode())

	getGone, err := apiClient.GetFileWithResponse(ctx, TestOrgSlug, fileUID)
	r.NoError(err)
	r.Equal(404, getGone.StatusCode())
}

// TestCLICoverage_EmailSuppressions exercises the listEmailSuppressions /
// deleteEmailSuppression generated-client operations against a seeded row.
func TestCLICoverage_EmailSuppressions(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()
	apiClient := cliCovAuthedClient(ctx, t, ts)

	// Seed an org-wide suppression directly.
	sup := models.NewEmailSuppression(
		cliCovOrgUID, "muted@example.com", nil, models.EmailSuppressionSourceLink)
	r.NoError(ts.Server.DBService().CreateEmailSuppression(ctx, sup))
	supUID := uuid.MustParse(sup.UID)

	// LIST — the seeded suppression is present.
	listResp, err := apiClient.ListEmailSuppressionsWithResponse(ctx, TestOrgSlug)
	r.NoError(err)
	r.Equal(200, listResp.StatusCode())
	r.NotNil(listResp.JSON200)
	r.NotNil(listResp.JSON200.Data)
	found := false
	for _, row := range *listResp.JSON200.Data {
		if row.Uid == supUID {
			found = true
			r.Equal("muted@example.com", row.Email)
			r.Equal(models.EmailSuppressionSourceLink, row.Source)
		}
	}
	r.True(found, "seeded suppression should appear in the list")

	// REMOVE — 204, then it is gone from the list.
	delResp, err := apiClient.DeleteEmailSuppressionWithResponse(ctx, TestOrgSlug, supUID)
	r.NoError(err)
	r.Equal(204, delResp.StatusCode())

	listAfter, err := apiClient.ListEmailSuppressionsWithResponse(ctx, TestOrgSlug)
	r.NoError(err)
	r.Equal(200, listAfter.StatusCode())
	if listAfter.JSON200.Data != nil {
		for _, row := range *listAfter.JSON200.Data {
			r.NotEqual(supUID, row.Uid, "suppression should be removed")
		}
	}
}
