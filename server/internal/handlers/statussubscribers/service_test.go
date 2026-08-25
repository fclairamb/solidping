package statussubscribers_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/statussubscribers"
)

type subSetup struct {
	svc   *statussubscribers.Service
	dbSvc *sqlite.Service
	org   *models.Organization
	page  *models.StatusPage
}

func newSubSetup(t *testing.T) *subSetup {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	page := models.NewStatusPage(org.UID, "Acme Status", "acme-status")
	r.NoError(dbSvc.CreateStatusPage(ctx, page))

	return &subSetup{
		svc: statussubscribers.NewService(dbSvc, nil,
			// The delivery tests point at an httptest server on 127.0.0.1.
			statussubscribers.WithAllowPrivateEndpoints(true)),
		dbSvc: dbSvc,
		org:   org,
		page:  page,
	}
}

func (s *subSetup) subscribe(t *testing.T, email string) *statussubscribers.SubscribeResult {
	t.Helper()

	res, err := s.svc.Subscribe(t.Context(), s.org.Slug, s.page.UID,
		&statussubscribers.SubscribeRequest{Email: email})
	require.NoError(t, err)

	return res
}

func TestSubscribeCreatesUnconfirmedRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSubSetup(t)

	res := s.subscribe(t, "user@example.com")
	r.NotNil(res.Subscriber)
	r.Equal("user@example.com", res.Subscriber.EmailAddress())
	r.Nil(res.Subscriber.ConfirmedAt)
	r.NotEmpty(res.Subscriber.ConfirmToken)
	r.NotEmpty(res.Subscriber.UnsubscribeToken)
	r.NotEqual(res.Subscriber.ConfirmToken, res.Subscriber.UnsubscribeToken)
}

func TestSubscribeNormalizesEmail(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSubSetup(t)

	res := s.subscribe(t, "  USER@Example.COM ")
	r.Equal("user@example.com", res.Subscriber.EmailAddress())
}

func TestSubscribeInvalidEmail(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSubSetup(t)

	_, err := s.svc.Subscribe(t.Context(), s.org.Slug, s.page.UID,
		&statussubscribers.SubscribeRequest{Email: "not-an-email"})
	r.ErrorIs(err, statussubscribers.ErrInvalidEmail)
}

func TestSubscribeDeduplicates(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSubSetup(t)
	ctx := t.Context()

	first := s.subscribe(t, "dup@example.com")
	second := s.subscribe(t, "dup@example.com")

	// Same logical subscription reused (same UID), tokens refreshed.
	r.Equal(first.Subscriber.UID, second.Subscriber.UID)
	r.NotEqual(first.Subscriber.ConfirmToken, second.Subscriber.ConfirmToken)

	all, err := s.dbSvc.ListSubscribers(ctx, s.page.UID)
	r.NoError(err)
	r.Len(all, 1)
}

func TestConfirmActivatesAndConsumesToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSubSetup(t)
	ctx := t.Context()

	res := s.subscribe(t, "confirm@example.com")
	token := res.Subscriber.ConfirmToken

	result, err := s.svc.Confirm(ctx, token)
	r.NoError(err)
	r.Equal(s.org.Slug, result.OrgSlug)
	r.Equal(s.page.Slug, result.PageSlug)

	// Row is now confirmed.
	sub, err := s.dbSvc.GetSubscriber(ctx, s.page.UID, res.Subscriber.UID)
	r.NoError(err)
	r.NotNil(sub.ConfirmedAt)

	// Token consumed: a second confirm with the same token fails.
	_, err = s.svc.Confirm(ctx, token)
	r.ErrorIs(err, statussubscribers.ErrInvalidToken)
}

func TestConfirmBadToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSubSetup(t)

	_, err := s.svc.Confirm(t.Context(), "nonexistent-token")
	r.ErrorIs(err, statussubscribers.ErrInvalidToken)

	_, err = s.svc.Confirm(t.Context(), "")
	r.ErrorIs(err, statussubscribers.ErrInvalidToken)
}

func TestUnsubscribeSoftDeletes(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSubSetup(t)
	ctx := t.Context()

	res := s.subscribe(t, "leave@example.com")
	_, err := s.svc.Confirm(ctx, res.Subscriber.ConfirmToken)
	r.NoError(err)

	_, err = s.svc.Unsubscribe(ctx, res.Subscriber.UnsubscribeToken)
	r.NoError(err)

	// No longer listed.
	all, err := s.dbSvc.ListSubscribers(ctx, s.page.UID)
	r.NoError(err)
	r.Empty(all)

	// Re-using the same unsub token now fails (row soft-deleted).
	_, err = s.svc.Unsubscribe(ctx, res.Subscriber.UnsubscribeToken)
	r.ErrorIs(err, statussubscribers.ErrInvalidToken)
}

func TestResubscribeAfterUnsubscribe(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSubSetup(t)
	ctx := t.Context()

	first := s.subscribe(t, "again@example.com")
	_, err := s.svc.Confirm(ctx, first.Subscriber.ConfirmToken)
	r.NoError(err)
	_, err = s.svc.Unsubscribe(ctx, first.Subscriber.UnsubscribeToken)
	r.NoError(err)

	// Re-subscribe should soft-undelete the same row, unconfirmed again.
	second := s.subscribe(t, "again@example.com")
	r.Equal(first.Subscriber.UID, second.Subscriber.UID)
	r.Nil(second.Subscriber.ConfirmedAt)

	all, err := s.dbSvc.ListSubscribers(ctx, s.page.UID)
	r.NoError(err)
	r.Len(all, 1)
}

func TestListConfirmedExcludesUnconfirmed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSubSetup(t)
	ctx := t.Context()

	confirmed := s.subscribe(t, "yes@example.com")
	_, err := s.svc.Confirm(ctx, confirmed.Subscriber.ConfirmToken)
	r.NoError(err)

	_ = s.subscribe(t, "no@example.com") // left unconfirmed

	subs, err := s.dbSvc.ListConfirmedSubscribers(ctx, s.page.UID, nil)
	r.NoError(err)
	r.Len(subs, 1)
	r.Equal("yes@example.com", subs[0].EmailAddress())
}

func TestAdminListAndRemove(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSubSetup(t)
	ctx := t.Context()

	res := s.subscribe(t, "admin-managed@example.com")

	list, err := s.svc.ListSubscribers(ctx, s.org.Slug, s.page.UID)
	r.NoError(err)
	r.Len(list, 1)
	r.Equal("admin-managed@example.com", list[0].Email)
	r.False(list[0].Confirmed)

	r.NoError(s.svc.RemoveSubscriber(ctx, s.org.Slug, s.page.UID, res.Subscriber.UID))

	after, err := s.svc.ListSubscribers(ctx, s.org.Slug, s.page.UID)
	r.NoError(err)
	r.Empty(after)

	// Removing a non-existent subscriber errors.
	err = s.svc.RemoveSubscriber(ctx, s.org.Slug, s.page.UID, "missing-uid")
	r.ErrorIs(err, statussubscribers.ErrSubscriberNotFound)
}

func TestSubscribeUnknownPage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newSubSetup(t)

	_, err := s.svc.Subscribe(t.Context(), s.org.Slug, "no-such-page",
		&statussubscribers.SubscribeRequest{Email: "x@example.com"})
	r.ErrorIs(err, statussubscribers.ErrStatusPageNotFound)
}
