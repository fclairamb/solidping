package sealing_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/sealing"
)

func TestRegionKeyCapabilityRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	caps := []string{"ipv6", sealing.RegionKeyCapability("7be0aa11bb22cc33"), "region-key-", "x-plugin"}
	r.Equal([]string{"7be0aa11bb22cc33"}, sealing.RegionKeyFingerprints(caps))
	r.NoError(models.ValidateCapabilitySet(caps[:2]), "the capability must be storable in workers.capabilities")
}

func TestBrowserSealingModeDefaultAndSet(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	mode, err := sealing.BrowserSealingMode(ctx, dbSvc, org.UID)
	r.NoError(err)
	r.Equal(sealing.ModeOptional, mode)

	r.NoError(sealing.SetBrowserSealingMode(ctx, dbSvc, org.UID, sealing.ModeRequired))
	mode, err = sealing.BrowserSealingMode(ctx, dbSvc, org.UID)
	r.NoError(err)
	r.Equal(sealing.ModeRequired, mode)

	r.ErrorIs(sealing.SetBrowserSealingMode(ctx, dbSvc, org.UID, "sometimes"), sealing.ErrInvalidMode)

	r.NoError(sealing.SetBrowserSealingMode(ctx, dbSvc, org.UID, sealing.ModeOptional))
	mode, err = sealing.BrowserSealingMode(ctx, dbSvc, org.UID)
	r.NoError(err)
	r.Equal(sealing.ModeOptional, mode)
}
