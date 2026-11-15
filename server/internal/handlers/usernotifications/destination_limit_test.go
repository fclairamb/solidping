package usernotifications

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/entitlements"
)

// Spec 2026-11-15-01: the per-contact resend limiter alone let an account
// probe any number of destinations by creating a new contact for each.

// addPhoneContact adds an unverified phone contact for user in org.
func addPhoneContact(t *testing.T, env *guardEnv, user *models.User, value string) *models.UserContact {
	t.Helper()

	contact := models.NewUserContact(user.UID, env.org.UID, models.UserContactTypePhone, value, "mobile")
	require.NoError(t, env.db.UpsertUserContact(context.Background(), contact))

	return contact
}

// agedUser returns a copy of user created long enough ago not to count as a
// young account.
func agedUser(user *models.User) *models.User {
	aged := *user
	aged.CreatedAt = time.Now().Add(-48 * time.Hour)

	return &aged
}

func TestDestinationLimiterUnit(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	limiter := newDestinationLimiter()
	now := time.Unix(1700000000, 0)

	r.True(limiter.allow("org-a", "+1", 2, now))
	r.True(limiter.allow("org-a", "+2", 2, now))
	r.False(limiter.allow("org-a", "+3", 2, now), "a third distinct destination is refused")
	r.True(limiter.allow("org-a", "+1", 2, now), "an already-counted destination does not consume a slot")
	r.True(limiter.allow("org-b", "+3", 2, now), "the limit is per org")

	later := now.Add(verifyDestinationWindow + time.Second)
	r.True(limiter.allow("org-a", "+3", 2, later), "slots free up after the window")
}

func TestDestinationLimiterConcurrent(t *testing.T) {
	t.Parallel()

	limiter := newDestinationLimiter()
	now := time.Unix(1700000000, 0)

	var allowed atomic.Int32

	var wg sync.WaitGroup
	for i := range 100 {
		wg.Go(func() {
			if limiter.allow("org-a", fmt.Sprintf("+%d", i), verifyDestinationMax, now) {
				allowed.Add(1)
			}
		})
	}
	wg.Wait()

	require.Equal(t, int32(verifyDestinationMax), allowed.Load())
}

// TestVerifyDistinctDestinationLimitPerOrg: an established account reaches
// verifyDestinationMax distinct destinations per org, counted across users.
func TestVerifyDistinctDestinationLimitPerOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupGuardEnv(t, false, "+15550000000", entitlements.WithInstanceSMSGuards(0, nil))
	owner := agedUser(env.user)

	other := models.NewUser("other@example.com")
	r.NoError(env.db.CreateUser(ctx, other))
	r.NoError(env.db.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(env.org.UID, other.UID, models.MemberRoleAdmin)))
	other = agedUser(other)

	r.NoError(env.svc.VerifyContact(ctx, env.org.Slug, owner, env.contact.UID))

	for i := 1; i < verifyDestinationMax; i++ {
		user := owner
		if i%2 == 0 {
			user = other // a second user of the same org shares the budget
		}

		contact := addPhoneContact(t, env, user, fmt.Sprintf("+1555000000%d", i))
		r.NoError(env.svc.VerifyContact(ctx, env.org.Slug, user, contact.UID), "destination %d", i)
	}

	extra := addPhoneContact(t, env, other, "+15550000099")
	r.ErrorIs(env.svc.VerifyContact(ctx, env.org.Slug, other, extra.UID), ErrResendTooSoon)
	r.Equal(verifyDestinationMax, env.instance.count(), "the refused destination reached no provider")

	// Re-sending to an already-counted destination still works.
	r.NoError(env.svc.VerifyContact(ctx, env.org.Slug, owner, env.contact.UID))
}

// TestVerifyDistinctDestinationLimitYoungAccount: a user created less than
// 24 h ago gets verifyDestinationMaxYoung.
func TestVerifyDistinctDestinationLimitYoungAccount(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupGuardEnv(t, false, "+15550000000", entitlements.WithInstanceSMSGuards(0, nil))
	r.Less(time.Since(env.user.CreatedAt), verifyYoungAccountAge)

	r.NoError(env.svc.VerifyContact(ctx, env.org.Slug, env.user, env.contact.UID))

	second := addPhoneContact(t, env, env.user, "+15550000001")
	r.NoError(env.svc.VerifyContact(ctx, env.org.Slug, env.user, second.UID))

	third := addPhoneContact(t, env, env.user, "+15550000002")
	r.ErrorIs(env.svc.VerifyContact(ctx, env.org.Slug, env.user, third.UID), ErrResendTooSoon)
	r.Equal(2, env.instance.count())

	// The same destination again is not a new slot (the resend limit applies).
	r.NoError(env.svc.VerifyContact(ctx, env.org.Slug, env.user, second.UID))
}

// TestVerifyConcurrentDistinctDestinations: concurrent requests never get more
// distinct destinations through than the limit.
func TestVerifyConcurrentDistinctDestinations(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupGuardEnv(t, false, "+15550000000", entitlements.WithInstanceSMSGuards(0, nil))
	owner := agedUser(env.user)

	contacts := make([]*models.UserContact, 0, 12)
	for i := range 12 {
		contacts = append(contacts, addPhoneContact(t, env, owner, fmt.Sprintf("+155500001%02d", i)))
	}

	var limited, unexpected atomic.Int32

	var wg sync.WaitGroup
	for _, contact := range contacts {
		wg.Go(func() {
			err := env.svc.VerifyContact(ctx, env.org.Slug, owner, contact.UID)

			switch {
			case err == nil:
			case errors.Is(err, ErrResendTooSoon):
				limited.Add(1)
			default:
				unexpected.Add(1)
			}
		})
	}
	wg.Wait()

	r.Zero(unexpected.Load())
	r.Equal(int32(len(contacts)-verifyDestinationMax), limited.Load())
	r.Equal(verifyDestinationMax, env.instance.count())
}

// TestVerifyBlockedWhileOrgFlaggedForProbing: after K allow-list refusals the
// org's verification sends are refused, even to an allowed destination, and
// nothing reaches the provider; the block lifts after an hour.
func TestVerifyBlockedWhileOrgFlaggedForProbing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	var (
		clockMu sync.Mutex
		now     = time.Now()
	)

	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()

		return now
	}

	var alerts atomic.Int32

	env := setupGuardEnv(t, false, "+8801711111111", func(s *entitlements.Service) {
		entitlements.WithInstanceSMSGuards(0, []string{"1"})(s)
		entitlements.WithClock(clock)(s)
		entitlements.WithSMSProbingNotifier(func(context.Context, *entitlements.SMSProbingAlert) {
			alerts.Add(1)
		})(s)
	})
	env.svc.clock = clock
	owner := agedUser(env.user)

	probes := []*models.UserContact{
		env.contact,
		addPhoneContact(t, env, owner, "+9981234567"),
		addPhoneContact(t, env, owner, "+8801722222222"),
	}
	for _, contact := range probes {
		r.ErrorIs(env.svc.VerifyContact(ctx, env.org.Slug, owner, contact.UID), ErrSMSDestinationNotAllowed)
	}

	r.Equal(int32(1), alerts.Load(), "the third refusal raises the ops alert")

	allowed := addPhoneContact(t, env, owner, "+15550000001")
	r.ErrorIs(env.svc.VerifyContact(ctx, env.org.Slug, owner, allowed.UID), ErrResendTooSoon)
	r.Equal(0, env.instance.count(), "a flagged org reaches no provider")

	clockMu.Lock()
	now = now.Add(time.Hour + time.Minute)
	clockMu.Unlock()

	r.NoError(env.svc.VerifyContact(ctx, env.org.Slug, owner, allowed.UID), "the block lifts after the hour")
	r.Equal(1, env.instance.count())
}
