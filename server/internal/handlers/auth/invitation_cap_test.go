package auth

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

func countEmailJobs(ctx context.Context, t *testing.T, dbSvc db.Service) int {
	t.Helper()

	listed, err := dbSvc.ListJobs(ctx, nil, 0)
	require.NoError(t, err)

	n := 0

	for _, job := range listed {
		if job.Type == string(jobdef.JobTypeEmail) {
			n++
		}
	}

	return n
}

func inviteRequestFor(email string) InviteRequest {
	return InviteRequest{
		Email:     email,
		Role:      string(models.MemberRoleUser),
		ExpiresIn: "24h",
		App:       appNameDash0,
	}
}

func fillInvitations(
	ctx context.Context, t *testing.T, svc *Service, slug, inviterUID, prefix string, n int,
) []*InviteResponse {
	t.Helper()

	out := make([]*InviteResponse, 0, n)

	for i := range n {
		resp, err := svc.CreateInvitation(ctx, slug, inviterUID, inviteRequestFor(fmt.Sprintf("%s%d@acme.com", prefix, i)))
		require.NoError(t, err)

		out = append(out, resp)
	}

	return out
}

func TestCreateInvitationCap(t *testing.T) {
	t.Parallel()

	svc, dbSvc, ctx := setupAuthTestService(t)
	svc.jobsSvc = jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	org, inviter := inviteHashFixture(t, dbSvc, "cap-org")

	invites := fillInvitations(ctx, t, svc, org.Slug, inviter.UID, "cap", maxLiveInvitationsPerOrg)

	// Positive control: accepted invitations enqueue their email.
	emailsBefore := countEmailJobs(ctx, t, dbSvc)
	require.Equal(t, maxLiveInvitationsPerOrg, emailsBefore, "each accepted invitation enqueues one email")

	_, err := svc.CreateInvitation(ctx, org.Slug, inviter.UID, inviteRequestFor("over@acme.com"))
	require.ErrorIs(t, err, ErrTooManyInvitations)
	require.Equal(t, emailsBefore, countEmailJobs(ctx, t, dbSvc), "a refused invitation enqueues no email")

	idx, getErr := dbSvc.GetStateEntry(ctx, nil, inviteEmailKey("over@acme.com", org.UID))
	require.NoError(t, getErr)
	require.Nil(t, idx, "a refused invitation leaves nothing behind")

	list, err := svc.ListInvitations(ctx, org.Slug)
	require.NoError(t, err)
	require.Len(t, list.Data, maxLiveInvitationsPerOrg)

	// Another org is not affected.
	other, otherInviter := inviteHashFixture(t, dbSvc, "cap-other")
	createHashTestInvite(t, svc, other.Slug, otherInviter.UID, "fine@acme.com")

	// Positive control: a revoked invitation frees a slot.
	require.NoError(t, svc.RevokeInvitation(ctx, org.Slug, invites[0].UID))
	_, err = svc.CreateInvitation(ctx, org.Slug, inviter.UID, inviteRequestFor("after-revoke@acme.com"))
	require.NoError(t, err)

	// An expired invitation frees a slot too.
	_, err = svc.CreateInvitation(ctx, org.Slug, inviter.UID, inviteRequestFor("still-over@acme.com"))
	require.ErrorIs(t, err, ErrTooManyInvitations)

	_, err = dbSvc.DB().NewUpdate().Model((*models.StateEntry)(nil)).
		Set("expires_at = ?", time.Now().Add(-time.Minute)).
		Where("organization_uid = ?", org.UID).
		Where("key = ?", inviteKeyPrefix+hashPendingToken(invites[1].Token)).
		Exec(ctx)
	require.NoError(t, err)
	_, err = svc.CreateInvitation(ctx, org.Slug, inviter.UID, inviteRequestFor("after-expiry@acme.com"))
	require.NoError(t, err)

	require.NotEmpty(t, invites)
}

type fixedSeats struct{ seats int }

func (f fixedSeats) CheckMembership(context.Context, string) error { return nil }

func (f fixedSeats) MaxUsers(context.Context, string) (*int, error) { return &f.seats, nil }

func TestCreateInvitationCapUsesLowerSeatCap(t *testing.T) {
	t.Parallel()

	svc, dbSvc, ctx := setupAuthTestService(t)
	svc.entitlements = fixedSeats{seats: 2}
	org, inviter := inviteHashFixture(t, dbSvc, "cap-seats")

	fillInvitations(ctx, t, svc, org.Slug, inviter.UID, "seat", 2)

	_, err := svc.CreateInvitation(ctx, org.Slug, inviter.UID, inviteRequestFor("third@acme.com"))
	require.ErrorIs(t, err, ErrTooManyInvitations)
}

func TestInviteEmailIndexLifecycle(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	org, inviter := inviteHashFixture(t, dbSvc, "idx-org")

	createHashTestInvite(t, svc, org.Slug, inviter.UID, "Idx@Acme.com")
	key := inviteEmailKey("idx@acme.com", org.UID)

	entry, err := dbSvc.GetStateEntry(ctx, nil, key)
	r.NoError(err)
	r.NotNil(entry, "the index is case-insensitive")

	// A second invitation to the same address takes over the index; revoking
	// the first must not drop it.
	second := createHashTestInvite(t, svc, org.Slug, inviter.UID, "idx@acme.com")

	list, err := svc.ListInvitations(ctx, org.Slug)
	r.NoError(err)
	r.Len(list.Data, 2)

	newerKey := inviteKeyPrefix + hashPendingToken(second.Token)

	var olderUID, newerUID string

	for _, item := range list.Data {
		stored, getErr := dbSvc.GetStateEntry(ctx, &org.UID, newerKey)
		r.NoError(getErr)

		if item.UID == stored.UID {
			newerUID = item.UID
		} else {
			olderUID = item.UID
		}
	}

	r.NoError(svc.RevokeInvitation(ctx, org.Slug, olderUID))

	entry, err = dbSvc.GetStateEntry(ctx, nil, key)
	r.NoError(err)
	r.NotNil(entry, "the newer invitation keeps the index entry")

	r.NoError(svc.RevokeInvitation(ctx, org.Slug, newerUID))

	entry, err = dbSvc.GetStateEntry(ctx, nil, key)
	r.NoError(err)
	r.Nil(entry, "revoking the last one drops the index entry")
}

func TestPersonalOrgInvitationLookupCost(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f, ctx := newPersonalOrgFixture(t)
	org, inviter := f.seededOrg(ctx, t, "pend-org", "pend-admin@acme.com")

	counter := &stateReadCounter{}
	f.db.DB().AddQueryHook(counter)

	signIn := func(email string) (*models.OrganizationMember, int64) {
		user := f.passwordUser(ctx, t, email, "Pending")

		var member *models.OrganizationMember

		n := counter.count(func() { member = f.svc.ensurePersonalOrg(ctx, user) })

		return member, n
	}

	// Positive control: a pending invitation is still honored.
	_, err := f.svc.CreateInvitation(ctx, org.Slug, inviter.UID, inviteRequestFor("invited@acme.com"))
	r.NoError(err)

	joined, _ := signIn("invited@acme.com")
	r.NotNil(joined)
	r.Equal(org.UID, joined.OrganizationUID)

	member, before := signIn("plain-1@acme.com")
	r.NotNil(member)
	r.GreaterOrEqual(before, int64(1), "positive control: the hook must fire")

	// Other orgs pile up invitations to other addresses.
	for i := range 3 {
		other, otherInviter := f.seededOrg(ctx, t, fmt.Sprintf("noise-%d", i), fmt.Sprintf("noise-admin-%d@acme.com", i))
		fillInvitations(ctx, t, f.svc, other.Slug, otherInviter.UID, fmt.Sprintf("noise%d-", i), 10)
	}

	_, after := signIn("plain-2@acme.com")
	r.LessOrEqual(after, before, "reads must not grow with other orgs' invitations")
}

func TestRegistrationReadsDoNotGrowWithOtherIPs(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupRegistrationLimitService(t, 5)

	counter := &stateReadCounter{}
	dbSvc.DB().AddQueryHook(counter)

	measure := func(email, ip string) int64 {
		return counter.count(func() { r.NoError(registerAMinuteLater(ctx, svc, email, ip)) })
	}

	before := measure("base@acme.com", limitTestIP)
	r.GreaterOrEqual(before, int64(1))

	for i := range 20 {
		r.NoError(registerAMinuteLater(ctx, svc, fmt.Sprintf("noise%d@acme.com", i), fmt.Sprintf("203.0.113.%d", i)))
	}

	after := measure("again@acme.com", limitTestIP)
	r.LessOrEqual(after, before, "reads must not grow with other IPs' pending registrations")
}

func TestConfirmRegistrationByTokenKey(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupRegistrationLimitService(t, 1)

	r.NoError(registerFromIP(ctx, svc, "conf@acme.com", limitTestIP))
	token := extractRegistrationConfirmToken(t, ctx, dbSvc, "conf@acme.com")
	r.NotEmpty(token)

	_, err := svc.ConfirmRegistration(ctx, "0000")
	r.ErrorIs(err, ErrRegistrationExpired)

	_, err = svc.ConfirmRegistration(ctx, token)
	r.NoError(err)

	// Confirming frees the IP slot and drops the keys.
	left, err := dbSvc.ListStateEntries(ctx, nil, registrationIPPrefix(limitTestIP))
	r.NoError(err)
	r.Empty(left)

	_, err = svc.ConfirmRegistration(ctx, token)
	r.ErrorIs(err, ErrRegistrationExpired)
}

// stateReadCounter counts the SELECTs on state_entries while armed.
type stateReadCounter struct{ inviteQueryCounter }

func (c *stateReadCounter) AfterQuery(_ context.Context, ev *bun.QueryEvent) {
	if c.armed.Load() && strings.HasPrefix(ev.Query, "SELECT") && strings.Contains(ev.Query, "state_entries") {
		c.total.Add(1)
	}
}
