package auth

// The org browser-sealing mode (spec 2026-11-08-01): `optional` when unset,
// PATCH stores `required` or goes back, anything else is refused first.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/sealing"
)

func TestOrgSettingsBrowserSealingMode(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)

	org := models.NewOrganization("sealing-a", "Sealing A")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	got, err := svc.GetOrgSettings(ctx, org.Slug)
	r.NoError(err)
	r.Equal(sealing.ModeOptional, got.BrowserSealingMode)

	required := sealing.ModeRequired
	updated, err := svc.UpdateOrgSettings(ctx, org.Slug, UpdateOrgSettingsRequest{BrowserSealingMode: &required})
	r.NoError(err)
	r.Equal(sealing.ModeRequired, updated.BrowserSealingMode)

	bad := "always"
	_, err = svc.UpdateOrgSettings(ctx, org.Slug, UpdateOrgSettingsRequest{BrowserSealingMode: &bad})
	r.ErrorIs(err, sealing.ErrInvalidMode)

	optional := sealing.ModeOptional
	back, err := svc.UpdateOrgSettings(ctx, org.Slug, UpdateOrgSettingsRequest{BrowserSealingMode: &optional})
	r.NoError(err)
	r.Equal(sealing.ModeOptional, back.BrowserSealingMode)
}
