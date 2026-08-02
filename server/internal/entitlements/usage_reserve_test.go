package entitlements

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

func setupReserveService(t *testing.T) (*Service, *models.Organization) {
	t.Helper()

	return setupReserveServiceWithOpts(t)
}

// setupReserveServiceWithOpts is setupReserveService with construction options,
// so a test can tighten a runaway cap without touching a shared global.
func setupReserveServiceWithOpts(t *testing.T, opts ...Option) (*Service, *models.Organization) {
	t.Helper()

	ctx := context.Background()
	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("res-org", "Res Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	svc := NewService(dbSvc, DefaultsFor(config.DeploymentModeSelfHosted), 0, opts...)

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
	r.ErrorAs(err, &qErr)
	r.Equal("MaxSmsPerMonth", qErr.LimitName)
}

// TestReserveSMS_RunawayCapConfigurable verifies WithRunawayCaps overrides the
// default hourly cap.
func TestReserveSMS_RunawayCapConfigurable(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("res-org", "Res Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// Override the SMS runaway cap to 3 (far below the default 30).
	svc := NewService(dbSvc, DefaultsFor(config.DeploymentModeSelfHosted), 0, WithRunawayCaps(3, 2))

	fixed := time.Now()
	svc.now = func() time.Time { return fixed }

	for i := 0; i < 3; i++ {
		r.NoError(svc.ReserveSMS(ctx, org.UID), "reservation %d within the overridden cap of 3", i)
	}
	r.ErrorIs(svc.ReserveSMS(ctx, org.UID), ErrEntitlementExceeded, "the 4th SMS must hit the overridden cap")

	// Voice cap overridden to 2 and enforced independently.
	for i := 0; i < 2; i++ {
		r.NoError(svc.ReserveCall(ctx, org.UID), "call %d within the overridden cap of 2", i)
	}
	r.ErrorIs(svc.ReserveCall(ctx, org.UID), ErrEntitlementExceeded, "the 3rd call must hit the overridden cap")
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

// TestReserveWhatsApp_MonthlyCap verifies the WhatsApp monthly cap is enforced
// through the same persistent counter as SMS, with a positive control: the
// first two reservations succeed, the third is refused, and the refusal names
// the WhatsApp limit rather than the SMS one.
func TestReserveWhatsApp_MonthlyCap(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	svc, org := setupReserveService(t)
	r.NoError(svc.Set(ctx, org.UID, Entitlements{
		Limits: Limits{MaxWhatsappPerMonth: Int(2)},
		Source: models.EntitlementSourceAdmin,
	}, "tester", "two per month"))

	r.NoError(svc.ReserveWhatsApp(ctx, org.UID))
	r.NoError(svc.ReserveWhatsApp(ctx, org.UID))

	err := svc.ReserveWhatsApp(ctx, org.UID)
	r.Error(err)

	var qErr *QuotaError
	r.ErrorAs(err, &qErr)
	r.Equal("MaxWhatsappPerMonth", qErr.LimitName)

	// The SMS cap is untouched by WhatsApp consumption: the two channels use
	// separate counters, so a WhatsApp burst must not exhaust SMS.
	r.NoError(svc.Set(ctx, org.UID, Entitlements{
		Limits: Limits{MaxSmsPerMonth: Int(1), MaxWhatsappPerMonth: Int(2)},
		Source: models.EntitlementSourceAdmin,
	}, "tester", "separate counters"))
	r.NoError(svc.ReserveSMS(ctx, org.UID))

	usage, usageErr := svc.Usage(ctx, org.UID)
	r.NoError(usageErr)
	r.Equal(2, usage.WhatsappThisMonth)
}

// TestReserveWhatsApp_RunawayGuard proves the hourly guard fires independently
// of the monthly entitlement, including for an unlimited (self-hosted) org.
func TestReserveWhatsApp_RunawayGuard(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	svc, org := setupReserveServiceWithOpts(t, WithWhatsAppRunawayCap(2))

	fixed := time.Now()
	svc.now = func() time.Time { return fixed }

	// No entitlement row: MaxWhatsappPerMonth stays nil = unlimited.
	r.NoError(svc.ReserveWhatsApp(ctx, org.UID))
	r.NoError(svc.ReserveWhatsApp(ctx, org.UID))

	err := svc.ReserveWhatsApp(ctx, org.UID)
	r.Error(err)

	var qErr *QuotaError
	r.ErrorAs(err, &qErr)
	r.Equal("whatsapp_runaway_per_hour", qErr.LimitName)
}
