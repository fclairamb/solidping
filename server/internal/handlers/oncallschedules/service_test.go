package oncallschedules_test

// Integration tests for the on-call service against a real (in-memory)
// sqlite backend. Pure-function rotation math is covered in
// resolver_test.go; this file exercises the parts that touch the DB:
// CRUD, override interaction with rotation, soft-deleted users, empty
// rosters, schedule-before-start_at.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/oncallschedules"
)

func newTestService(t *testing.T) (*oncallschedules.Service, db.Service, *models.Organization) {
	t.Helper()

	svc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = svc.Close()
	})

	require.NoError(t, svc.Initialize(t.Context()))

	org := models.NewOrganization("test", "")
	require.NoError(t, svc.CreateOrganization(t.Context(), org))

	onCall := oncallschedules.NewService(svc)

	return onCall, svc, org
}

func newTestUser(t *testing.T, dbSvc db.Service, _, name string) *models.User {
	t.Helper()

	user := models.NewUser(name + "@test.com")
	user.Name = name
	require.NoError(t, dbSvc.CreateUser(t.Context(), user))

	return user
}

func TestServiceCreateScheduleHappyPath(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := newTestService(t)

	alice := newTestUser(t, dbSvc, org.UID, "alice")
	bob := newTestUser(t, dbSvc, org.UID, "bob")

	mon := 0
	startAt := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	schedule, err := svc.CreateSchedule(t.Context(), &oncallschedules.CreateScheduleInput{
		OrganizationUID: org.UID,
		Slug:            "platform",
		Name:            "Platform",
		Timezone:        "Europe/Paris",
		RotationType:    models.RotationTypeWeekly,
		HandoffTime:     "09:00",
		HandoffWeekday:  &mon,
		StartAt:         startAt,
		UserUIDs:        []string{alice.UID, bob.UID},
	})
	r.NoError(err)
	r.Equal("platform", schedule.Slug)

	roster, err := svc.ListUsers(t.Context(), schedule.UID)
	r.NoError(err)
	r.Len(roster, 2)
	r.Equal(alice.UID, roster[0].UserUID)
	r.Equal(bob.UID, roster[1].UserUID)
	r.Equal(0, roster[0].Position)
	r.Equal(1, roster[1].Position)
}

func TestServiceCreateScheduleWeeklyRequiresWeekday(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, _, org := newTestService(t)

	_, err := svc.CreateSchedule(t.Context(), &oncallschedules.CreateScheduleInput{
		OrganizationUID: org.UID,
		Slug:            "x",
		Name:            "X",
		Timezone:        "Europe/Paris",
		RotationType:    models.RotationTypeWeekly,
		HandoffTime:     "09:00",
		StartAt:         time.Now(),
	})
	r.ErrorIs(err, oncallschedules.ErrWeekdayRequired)
}

func TestServiceCreateScheduleDailyRejectsWeekday(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, _, org := newTestService(t)

	mon := 0
	_, err := svc.CreateSchedule(t.Context(), &oncallschedules.CreateScheduleInput{
		OrganizationUID: org.UID,
		Slug:            "x",
		Name:            "X",
		Timezone:        "Europe/Paris",
		RotationType:    models.RotationTypeDaily,
		HandoffTime:     "09:00",
		HandoffWeekday:  &mon,
		StartAt:         time.Now(),
	})
	r.ErrorIs(err, oncallschedules.ErrWeekdayUnused)
}

func TestServiceCreateScheduleRejectsBadTimezone(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, _, org := newTestService(t)

	_, err := svc.CreateSchedule(t.Context(), &oncallschedules.CreateScheduleInput{
		OrganizationUID: org.UID,
		Slug:            "x",
		Name:            "X",
		Timezone:        "Not/A/Real/Zone",
		RotationType:    models.RotationTypeDaily,
		HandoffTime:     "09:00",
		StartAt:         time.Now(),
	})
	r.ErrorIs(err, oncallschedules.ErrInvalidTimezone)
}

func TestServiceResolveBeforeStartAt(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := newTestService(t)

	alice := newTestUser(t, dbSvc, org.UID, "alice")

	startAt := time.Date(2030, 1, 1, 9, 0, 0, 0, time.UTC)
	mon := 0
	schedule, err := svc.CreateSchedule(t.Context(), &oncallschedules.CreateScheduleInput{
		OrganizationUID: org.UID,
		Slug:            "future",
		Name:            "Future",
		Timezone:        "Europe/Paris",
		RotationType:    models.RotationTypeWeekly,
		HandoffTime:     "09:00",
		HandoffWeekday:  &mon,
		StartAt:         startAt,
		UserUIDs:        []string{alice.UID},
	})
	r.NoError(err)

	_, err = svc.Resolve(t.Context(), schedule.UID, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	r.ErrorIs(err, oncallschedules.ErrScheduleNotYetActive)
}

func TestServiceResolveEmptyRoster(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, _, org := newTestService(t)

	mon := 0
	startAt := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	schedule, err := svc.CreateSchedule(t.Context(), &oncallschedules.CreateScheduleInput{
		OrganizationUID: org.UID,
		Slug:            "empty",
		Name:            "Empty",
		Timezone:        "Europe/Paris",
		RotationType:    models.RotationTypeWeekly,
		HandoffTime:     "09:00",
		HandoffWeekday:  &mon,
		StartAt:         startAt,
		UserUIDs:        nil,
	})
	r.NoError(err)

	_, err = svc.Resolve(t.Context(), schedule.UID, startAt.Add(time.Hour))
	r.ErrorIs(err, oncallschedules.ErrScheduleHasNoUsers)
}

func TestServiceResolveOverrideWins(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := newTestService(t)

	alice := newTestUser(t, dbSvc, org.UID, "alice")
	bob := newTestUser(t, dbSvc, org.UID, "bob")
	carol := newTestUser(t, dbSvc, org.UID, "carol")

	mon := 0
	startAt := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	schedule, err := svc.CreateSchedule(t.Context(), &oncallschedules.CreateScheduleInput{
		OrganizationUID: org.UID,
		Slug:            "platform",
		Name:            "Platform",
		Timezone:        "Europe/Paris",
		RotationType:    models.RotationTypeWeekly,
		HandoffTime:     "09:00",
		HandoffWeekday:  &mon,
		StartAt:         startAt,
		UserUIDs:        []string{alice.UID, bob.UID},
	})
	r.NoError(err)

	// Without an override the rotation puts alice on call at startAt.
	user, err := svc.Resolve(t.Context(), schedule.UID, startAt.Add(time.Hour))
	r.NoError(err)
	r.Equal(alice.UID, user.UID)

	// Override [startAt+30m, startAt+2h) → carol.
	overrideStart := startAt.Add(30 * time.Minute)
	overrideEnd := startAt.Add(2 * time.Hour)
	_, err = svc.CreateOverride(t.Context(), &oncallschedules.CreateOverrideInput{
		ScheduleUID: schedule.UID,
		UserUID:     carol.UID,
		StartAt:     overrideStart,
		EndAt:       overrideEnd,
	})
	r.NoError(err)

	// Inside the override window — carol.
	user, err = svc.Resolve(t.Context(), schedule.UID, startAt.Add(time.Hour))
	r.NoError(err)
	r.Equal(carol.UID, user.UID)

	// At the override end (exclusive) — back to alice.
	user, err = svc.Resolve(t.Context(), schedule.UID, overrideEnd)
	r.NoError(err)
	r.Equal(alice.UID, user.UID)
}

func TestServiceResolveOverlappingOverridesMostRecentWins(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := newTestService(t)

	alice := newTestUser(t, dbSvc, org.UID, "alice")
	bob := newTestUser(t, dbSvc, org.UID, "bob")
	carol := newTestUser(t, dbSvc, org.UID, "carol")

	mon := 0
	startAt := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	schedule, err := svc.CreateSchedule(t.Context(), &oncallschedules.CreateScheduleInput{
		OrganizationUID: org.UID,
		Slug:            "platform",
		Name:            "Platform",
		Timezone:        "Europe/Paris",
		RotationType:    models.RotationTypeWeekly,
		HandoffTime:     "09:00",
		HandoffWeekday:  &mon,
		StartAt:         startAt,
		UserUIDs:        []string{alice.UID},
	})
	r.NoError(err)

	overrideStart := startAt.Add(time.Hour)
	overrideEnd := startAt.Add(3 * time.Hour)

	// First override: bob.
	_, err = svc.CreateOverride(t.Context(), &oncallschedules.CreateOverrideInput{
		ScheduleUID: schedule.UID,
		UserUID:     bob.UID,
		StartAt:     overrideStart,
		EndAt:       overrideEnd,
	})
	r.NoError(err)

	// Second (overlapping) override: carol — created later, must win.
	// Sleep one second so the created_at strictly orders.
	time.Sleep(time.Second)

	_, err = svc.CreateOverride(t.Context(), &oncallschedules.CreateOverrideInput{
		ScheduleUID: schedule.UID,
		UserUID:     carol.UID,
		StartAt:     overrideStart,
		EndAt:       overrideEnd,
	})
	r.NoError(err)

	user, err := svc.Resolve(t.Context(), schedule.UID, startAt.Add(2*time.Hour))
	r.NoError(err)
	r.Equal(carol.UID, user.UID, "most recently created override should win")
}

func TestServiceResolveSkipsSoftDeletedUser(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := newTestService(t)

	alice := newTestUser(t, dbSvc, org.UID, "alice")
	bob := newTestUser(t, dbSvc, org.UID, "bob")

	mon := 0
	startAt := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	schedule, err := svc.CreateSchedule(t.Context(), &oncallschedules.CreateScheduleInput{
		OrganizationUID: org.UID,
		Slug:            "platform",
		Name:            "Platform",
		Timezone:        "Europe/Paris",
		RotationType:    models.RotationTypeWeekly,
		HandoffTime:     "09:00",
		HandoffWeekday:  &mon,
		StartAt:         startAt,
		UserUIDs:        []string{alice.UID, bob.UID},
	})
	r.NoError(err)

	// Alice on call at start. Soft-delete her — Bob should take over.
	require.NoError(t, dbSvc.DeleteUser(t.Context(), alice.UID))

	user, err := svc.Resolve(t.Context(), schedule.UID, startAt.Add(time.Hour))
	r.NoError(err)
	r.Equal(bob.UID, user.UID, "soft-deleted user is skipped")
}

func TestServiceCreateOverrideEndBeforeStart(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := newTestService(t)

	alice := newTestUser(t, dbSvc, org.UID, "alice")

	mon := 0
	schedule, err := svc.CreateSchedule(t.Context(), &oncallschedules.CreateScheduleInput{
		OrganizationUID: org.UID,
		Slug:            "x",
		Name:            "X",
		Timezone:        "Europe/Paris",
		RotationType:    models.RotationTypeWeekly,
		HandoffTime:     "09:00",
		HandoffWeekday:  &mon,
		StartAt:         time.Now(),
		UserUIDs:        []string{alice.UID},
	})
	r.NoError(err)

	now := time.Now()
	_, err = svc.CreateOverride(t.Context(), &oncallschedules.CreateOverrideInput{
		ScheduleUID: schedule.UID,
		UserUID:     alice.UID,
		StartAt:     now,
		EndAt:       now.Add(-time.Hour),
	})
	r.ErrorIs(err, oncallschedules.ErrOverrideEndBeforeStart)
}

func TestServiceICalFeedEnableDisableRotate(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := newTestService(t)

	alice := newTestUser(t, dbSvc, org.UID, "alice")

	mon := 0
	schedule, err := svc.CreateSchedule(t.Context(), &oncallschedules.CreateScheduleInput{
		OrganizationUID: org.UID,
		Slug:            "x",
		Name:            "X",
		Timezone:        "Europe/Paris",
		RotationType:    models.RotationTypeWeekly,
		HandoffTime:     "09:00",
		HandoffWeekday:  &mon,
		StartAt:         time.Now(),
		UserUIDs:        []string{alice.UID},
	})
	r.NoError(err)

	secret1, err := svc.EnableICalFeed(t.Context(), org.UID, schedule.UID)
	r.NoError(err)
	r.NotEmpty(secret1)

	// LookupScheduleByICalSecret resolves the secret back to the schedule.
	got, err := svc.LookupScheduleByICalSecret(t.Context(), secret1)
	r.NoError(err)
	r.Equal(schedule.UID, got.UID)

	// Rotate → new secret, the old one stops working.
	secret2, err := svc.RotateICalFeed(t.Context(), org.UID, schedule.UID)
	r.NoError(err)
	r.NotEqual(secret1, secret2)
	_, err = svc.LookupScheduleByICalSecret(t.Context(), secret1)
	r.ErrorIs(err, oncallschedules.ErrScheduleNotFound)

	// Disable → new secret stops working too.
	r.NoError(svc.DisableICalFeed(t.Context(), org.UID, schedule.UID))
	_, err = svc.LookupScheduleByICalSecret(t.Context(), secret2)
	r.ErrorIs(err, oncallschedules.ErrScheduleNotFound)
}

func TestServicePreviewProducesContiguousSlots(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := newTestService(t)

	alice := newTestUser(t, dbSvc, org.UID, "alice")
	bob := newTestUser(t, dbSvc, org.UID, "bob")

	mon := 0
	startAt := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	schedule, err := svc.CreateSchedule(t.Context(), &oncallschedules.CreateScheduleInput{
		OrganizationUID: org.UID,
		Slug:            "platform",
		Name:            "Platform",
		Timezone:        "Europe/Paris",
		RotationType:    models.RotationTypeWeekly,
		HandoffTime:     "09:00",
		HandoffWeekday:  &mon,
		StartAt:         startAt,
		UserUIDs:        []string{alice.UID, bob.UID},
	})
	r.NoError(err)

	slots, err := svc.Preview(context.Background(), schedule.UID, startAt, 14*24*time.Hour)
	r.NoError(err)
	r.GreaterOrEqual(len(slots), 2, "two weeks of weekly rotation produces at least 2 slots")

	// Slots are contiguous: each slot's End == next slot's From.
	for i := 1; i < len(slots); i++ {
		r.Equal(slots[i-1].To, slots[i].From, "slot %d boundary mismatch", i)
	}

	// User assignment alternates with each handoff.
	r.Equal(alice.UID, slots[0].UserUID)
	if len(slots) >= 2 {
		r.Equal(bob.UID, slots[1].UserUID)
	}
}
