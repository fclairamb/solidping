package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
)

// errDispatcherDown is the static stand-in for "the job queue is down".
var errDispatcherDown = errors.New("the job queue is down")

// noticeDispatcherMu serializes the tests that install the process-global
// notice dispatcher. They still declare t.Parallel(); the lock queues them.
var noticeDispatcherMu sync.Mutex //nolint:gochecknoglobals // test-local serialization of a process-wide hook

// noticeCollector records the notices raised while it is installed, filtered by
// a per-test marker: the dispatcher is process-global and the rest of this
// package's tests run in parallel, so an unfiltered collector would also record
// their signups.
type noticeCollector struct {
	mu      sync.Mutex
	marker  string
	notices []*opsnotify.Notice
}

func collectSignupNotices(t *testing.T, marker string) *noticeCollector {
	t.Helper()

	c := &noticeCollector{marker: marker}

	noticeDispatcherMu.Lock()
	t.Cleanup(func() {
		opsnotify.SetDispatcher(nil)
		noticeDispatcherMu.Unlock()
	})

	opsnotify.SetDispatcher(func(_ context.Context, notice *opsnotify.Notice) error {
		if !strings.Contains(notice.Subject, c.marker) {
			return nil
		}

		c.mu.Lock()
		defer c.mu.Unlock()

		c.notices = append(c.notices, notice)

		return nil
	})

	return c
}

func (c *noticeCollector) all() []*opsnotify.Notice {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]*opsnotify.Notice(nil), c.notices...)
}

// TestSignupRaisesAUserRegisteredNotice walks the whole password signup — the
// only path that can be driven end to end from here — and proves the notice
// carries what an operator needs to act: who, and how they got in.
func TestSignupRaisesAUserRegisteredNotice(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const email = "notice-registrant@acme.com"

	notices := collectSignupNotices(t, email)
	f, ctx := newLoginAuditFixture(t)
	f.svc.fullCfg.Auth.RegistrationEmailPattern = ".*"

	_, err := f.svc.Register(ctx, RegisterRequest{
		Name: "Alice", Email: email, Password: "supersecret123",
	})
	r.NoError(err)

	entries, err := f.db.ListStateEntries(ctx, nil, registrationKeyPrefix)
	r.NoError(err)

	var token string

	for _, entry := range entries {
		if entry.Value == nil {
			continue
		}

		if got, ok := (*entry.Value)[keyEmail].(string); ok && got == email {
			token, _ = (*entry.Value)[keyToken].(string)
		}
	}

	r.NotEmpty(token, "precondition: the registration token must have been stored")

	_, err = f.svc.ConfirmRegistration(ctx, token)
	r.NoError(err)

	raised := notices.all()
	r.Len(raised, 1, "one account created is one notice")
	r.Equal(opsnotify.EventUserRegistered, raised[0].Event)
	r.Contains(raised[0].Subject, email)
	r.Contains(raised[0].Subject, signupMethodPassword)
	r.Contains(raised[0].Body, email, "the email travels deliberately; recipients are super admins")
	r.Contains(raised[0].Body, "Alice")
	r.Contains(raised[0].Body, "Method: "+signupMethodPassword)
	r.NotEmpty(raised[0].AboutUserUID,
		"the landing org is resolved at delivery time and needs the subject's uid")
}

// TestSignupNoticeCarriesEveryMethod covers the other signup families through
// the single chokepoint they all funnel into.
//
// Driving each OAuth/SAML/LDAP provider end to end would need a fake IdP per
// family and would still exercise this exact call; the source-level guard in
// signup_analytics_test.go is what proves none of them bypasses it.
func TestSignupNoticeCarriesEveryMethod(t *testing.T) {
	t.Parallel()

	for _, method := range []string{
		signupMethodPassword, signupMethodInvite, signupMethodGoogle,
		signupMethodGitHub, SignupMethodSlack, signupMethodOIDC, signupMethodLDAP,
	} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			email := "signup-" + method + "@acme.com"
			notices := collectSignupNotices(t, email)
			f, ctx := newLoginAuditFixture(t)

			user := models.NewUser(email)
			user.Name = "Bob"
			r.NoError(createUserAndCapture(ctx, f.db, user, method))

			raised := notices.all()
			r.Len(raised, 1)
			r.Equal(opsnotify.EventUserRegistered, raised[0].Event)
			r.Contains(raised[0].Body, "Method: "+method,
				"the method is what lets a recipient tell an invite from an organic signup")
			r.Equal(user.UID, raised[0].AboutUserUID)
		})
	}
}

// TestSignupSurvivesAFailingNoticeDispatcher is the negative case the spec
// calls out, proved directly rather than asserted: with the hand-off stubbed to
// ERROR, creating the account must still succeed and the row must still exist.
//
// A signup that fails because a messaging provider is down would be a far worse
// bug than the missing notification this feature exists to add.
func TestSignupSurvivesAFailingNoticeDispatcher(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	noticeDispatcherMu.Lock()
	t.Cleanup(func() {
		opsnotify.SetDispatcher(nil)
		noticeDispatcherMu.Unlock()
	})

	var calls int

	opsnotify.SetDispatcher(func(context.Context, *opsnotify.Notice) error {
		calls++

		return errDispatcherDown
	})

	f, ctx := newLoginAuditFixture(t)

	user := models.NewUser("dispatcher-down@acme.com")
	r.NoError(createUserAndCapture(ctx, f.db, user, signupMethodPassword),
		"an erroring notice dispatcher must never fail the signup")

	stored, err := f.db.GetUser(ctx, user.UID)
	r.NoError(err)
	r.NotNil(stored, "the account exists regardless of the notice")

	r.Positive(calls, "precondition: the dispatcher really was consulted")
}

// TestSignupSurvivesAPanickingNoticeDispatcher is the harsher half of the same
// contract.
func TestSignupSurvivesAPanickingNoticeDispatcher(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	noticeDispatcherMu.Lock()
	t.Cleanup(func() {
		opsnotify.SetDispatcher(nil)
		noticeDispatcherMu.Unlock()
	})

	opsnotify.SetDispatcher(func(context.Context, *opsnotify.Notice) error {
		panic("the job queue exploded")
	})

	f, ctx := newLoginAuditFixture(t)

	user := models.NewUser("dispatcher-panic@acme.com")
	r.NoError(createUserAndCapture(ctx, f.db, user, signupMethodPassword))

	stored, err := f.db.GetUser(ctx, user.UID)
	r.NoError(err)
	r.NotNil(stored)
}
