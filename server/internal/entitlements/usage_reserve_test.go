package entitlements

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

func setupReserveService(t *testing.T) (*Service, *models.Organization) {
	t.Helper()

	ctx := context.Background()
	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("res-org", "Res Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	svc := NewService(dbSvc, DefaultsFor(config.DeploymentModeSelfHosted), 0)

	return svc, org
}

// TestReserveSMS_UnlimitedRunawayGuard verifies self-hosted (unlimited monthly)
// still stops at the hourly runaway cap.
func TestReserveSMS_UnlimitedRunawayGuard(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	svc, org := setupReserveService(t)

	// Freeze time so the bucket never refills mid-test.
	fixed := time.Now()
	svc.now = func() time.Time { return fixed }

	for i := 0; i < defaultSMSRunawayPerHour; i++ {
		r.NoError(svc.ReserveSMS(ctx, org.UID), "reservation %d within runaway cap", i)
	}

	err := svc.ReserveSMS(ctx, org.UID)
	r.Error(err, "the runaway cap must deny the next reservation")
	r.ErrorIs(err, ErrEntitlementExceeded)
}

// TestReserveSMS_MonthlyCap verifies a finite MaxSmsPerMonth is enforced via the
// persistent counter.
func TestReserveSMS_MonthlyCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	svc, org := setupReserveService(t)

	r.NoError(svc.Set(ctx, org.UID, Entitlements{
		Limits: Limits{MaxSmsPerMonth: Int(2)},
		Source: models.EntitlementSourceAdmin,
	}, "tester", "test cap"))

	r.NoError(svc.ReserveSMS(ctx, org.UID))
	r.NoError(svc.ReserveSMS(ctx, org.UID))

	err := svc.ReserveSMS(ctx, org.UID)
	r.Error(err, "third SMS must exceed the monthly cap of 2")
	r.ErrorIs(err, ErrEntitlementExceeded)

	var qErr *QuotaError
	r.True(errors.As(err, &qErr))
	r.Equal("MaxSmsPerMonth", qErr.LimitName)
}

// TestReserveCall_ZeroLimitDenies verifies a 0 monthly cap denies immediately.
func TestReserveCall_ZeroLimitDenies(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	svc, org := setupReserveService(t)

	r.NoError(svc.Set(ctx, org.UID, Entitlements{
		Limits: Limits{MaxCallsPerMonth: Int(0)},
		Source: models.EntitlementSourceAdmin,
	}, "tester", "no voice"))

	err := svc.ReserveCall(ctx, org.UID)
	r.ErrorIs(err, ErrEntitlementExceeded)
}
