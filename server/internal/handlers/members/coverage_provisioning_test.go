package members_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/members"
)

// capturingEmailSender records the last message instead of sending it.
type capturingEmailSender struct {
	last *email.Message
	sent int
}

func (s *capturingEmailSender) Send(_ context.Context, msg *email.Message) (*email.SendResult, error) {
	s.last = msg
	s.sent++

	return &email.SendResult{}, nil
}

type memberFixture struct {
	svc    *members.Service
	dbSvc  db.Service
	org    *models.Organization
	mailer *capturingEmailSender
}

func newMemberFixture(ctx context.Context, t *testing.T, slug string) *memberFixture {
	t.Helper()

	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization(slug, "Coverage Test Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	mailer := &capturingEmailSender{}
	svc := members.NewService(dbSvc,
		members.WithEmailSender(mailer),
		members.WithAppBaseURL("https://solidping.example/"))

	return &memberFixture{svc: svc, dbSvc: dbSvc, org: org, mailer: mailer}
}

// addMember creates a user + membership and returns both.
func (f *memberFixture) addMember(
	ctx context.Context, t *testing.T, email, name string,
) (*models.User, *models.OrganizationMember) {
	t.Helper()

	user := models.NewUser(email)
	user.Name = name
	require.NoError(t, f.dbSvc.CreateUser(ctx, user))

	member := models.NewOrganizationMember(f.org.UID, user.UID, models.MemberRoleUser)
	require.NoError(t, f.dbSvc.CreateOrganizationMember(ctx, member))

	return user, member
}

// addContact writes a contact + route for a user, optionally verified.
func (f *memberFixture) addContact(
	ctx context.Context, t *testing.T, user *models.User, contactType, value string, verified bool,
) *models.UserContact {
	t.Helper()

	contact := models.NewUserContact(user.UID, f.org.UID, contactType, value, "")
	if verified {
		now := time.Now()
		contact.VerifiedAt = &now
	}

	require.NoError(t, f.dbSvc.UpsertUserContact(ctx, contact))
	require.NoError(t, f.dbSvc.EnsureUserNotificationRoute(ctx, user.UID, f.org.UID, contact.UID))

	return contact
}

func coverageFor(
	entries []*members.MemberCoverageResponse, userUID string,
) *members.MemberCoverageResponse {
	for _, entry := range entries {
		if entry.UserUID == userUID {
			return entry
		}
	}

	return nil
}

// TestListCoverageFlagsEmailOnlyMembers is the Phase 2 signal: an admin must be
// able to see, before rostering somebody on call, that nothing but email can
// reach them.
func TestListCoverageFlagsEmailOnlyMembers(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMemberFixture(ctx, t, "coverage-flags")

	bare, _ := fx.addMember(ctx, t, "bare@acme.test", "Bare")
	emailOnly, _ := fx.addMember(ctx, t, "emailonly@acme.test", "Email Only")
	unverified, _ := fx.addMember(ctx, t, "unverified@acme.test", "Unverified")
	paged, _ := fx.addMember(ctx, t, "paged@acme.test", "Paged")

	fx.addContact(ctx, t, emailOnly, models.UserContactTypeEmail, "emailonly@acme.test", true)
	// An unverified phone is NOT paging coverage — verification is what makes a
	// number pageable, so this member must still read as email-only.
	fx.addContact(ctx, t, unverified, models.UserContactTypePhone, "+15550001111", false)
	fx.addContact(ctx, t, paged, models.UserContactTypePhone, "+15550002222", true)

	resp, err := fx.svc.ListCoverage(ctx, fx.org.Slug)
	r.NoError(err)
	r.Len(resp.Data, 4)

	r.True(coverageFor(resp.Data, bare.UID).EmailFallbackOnly)
	r.True(coverageFor(resp.Data, emailOnly.UID).EmailFallbackOnly)
	r.True(coverageFor(resp.Data, unverified.UID).EmailFallbackOnly)
	r.False(coverageFor(resp.Data, paged.UID).EmailFallbackOnly)

	// The unverified member's channel is still listed — the admin needs to see
	// "phone, unverified" in order to chase them.
	channels := coverageFor(resp.Data, unverified.UID).Channels
	r.Len(channels, 1)
	r.Equal(models.UserContactTypePhone, channels[0].Type)
	r.False(channels[0].Verified)
}

// TestListCoverageNeverExposesContactValues is a privacy guard: admins get
// channel types and flags, never the phone number itself.
func TestListCoverageNeverExposesContactValues(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMemberFixture(ctx, t, "coverage-privacy")

	user, _ := fx.addMember(ctx, t, "user@acme.test", "User")
	fx.addContact(ctx, t, user, models.UserContactTypePhone, "+15550009999", true)

	resp, err := fx.svc.ListCoverage(ctx, fx.org.Slug)
	r.NoError(err)

	encoded := mustJSON(t, resp)
	r.NotContains(encoded, "+15550009999")
	r.Contains(encoded, models.UserContactTypePhone)
}

// TestAdminCannotCreateVerifiedContact is the Phase 3 invariant, stated as a
// negative test: whatever an admin does through this endpoint, the resulting
// contact is unverified and therefore not pageable.
func TestAdminCannotCreateVerifiedContact(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMemberFixture(ctx, t, "provision-unverified")

	user, member := fx.addMember(ctx, t, "colleague@acme.test", "Colleague")

	created, err := fx.svc.AddMemberContact(ctx, fx.org.Slug, member.UID,
		members.AdminAddContactRequest{Type: models.UserContactTypePhone, Value: "+15551230000"})
	r.NoError(err)
	r.False(created.Verified)

	stored, err := fx.dbSvc.GetUserContact(ctx, created.UID)
	r.NoError(err)
	r.Nil(stored.VerifiedAt, "an admin-provisioned contact must never carry a verified stamp")

	// The route exists, so the member sees it in their own settings and can
	// verify it — but it is not verified, hence not pageable.
	routes, err := fx.dbSvc.ListUserContactsWithRoutes(ctx, user.UID, fx.org.UID)
	r.NoError(err)

	found := false

	for _, route := range routes {
		if route.Contact != nil && route.Contact.UID == created.UID {
			found = true

			r.Nil(route.Contact.VerifiedAt)
		}
	}

	r.True(found, "the provisioned contact must be routed so its owner can verify it")
}

// TestAdminCannotFlipExistingContactToVerified: the admin path refuses an
// already-present contact outright rather than updating it, so there is no
// shape of request that promotes an unverified contact to verified.
func TestAdminCannotFlipExistingContactToVerified(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMemberFixture(ctx, t, "provision-noflip")

	user, member := fx.addMember(ctx, t, "colleague@acme.test", "Colleague")
	existing := fx.addContact(ctx, t, user, models.UserContactTypePhone, "+15551230000", false)

	_, err := fx.svc.AddMemberContact(ctx, fx.org.Slug, member.UID,
		members.AdminAddContactRequest{Type: models.UserContactTypePhone, Value: "+15551230000"})
	r.ErrorIs(err, members.ErrContactAlreadyExists)

	stored, err := fx.dbSvc.GetUserContact(ctx, existing.UID)
	r.NoError(err)
	r.Nil(stored.VerifiedAt)
}

// TestAdminCannotDeVerifyRoutelessContact is the regression test for a real
// downgrade bug: the duplicate guard used to read the ROUTE-JOINED contact list,
// so a live contact with no route was invisible to it. The add then fell through
// to the upsert, which matched the (user, org, type, value) unique key and landed
// on that very row, and the unconditional verified-stamp clear wiped a stamp the
// member had earned — silently making them unpageable on a number that worked.
//
// A contact goes routeless whenever the member removes the route but keeps the
// number, so this is an ordinary state, not a corrupt one.
func TestAdminCannotDeVerifyRoutelessContact(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMemberFixture(ctx, t, "provision-routeless")

	user, member := fx.addMember(ctx, t, "colleague@acme.test", "Colleague")

	// A VERIFIED contact with deliberately NO route.
	verifiedAt := time.Now().UTC().Truncate(time.Second)
	contact := models.NewUserContact(
		user.UID, fx.org.UID, models.UserContactTypePhone, "+15557654321", "")
	contact.VerifiedAt = &verifiedAt
	r.NoError(fx.dbSvc.UpsertUserContact(ctx, contact))

	// Guard the premise: this contact really is invisible to the route-joined
	// view, so the test exercises the path the bug lived on rather than passing
	// for an unrelated reason.
	routed, err := fx.dbSvc.ListUserContactsWithRoutes(ctx, user.UID, fx.org.UID)
	r.NoError(err)

	for _, route := range routed {
		if route.Contact != nil {
			r.NotEqual("+15557654321", route.Contact.Value,
				"premise broken: the fixture contact must have no route")
		}
	}

	// The admin "sets up paging" with the number the member already verified.
	_, err = fx.svc.AddMemberContact(ctx, fx.org.Slug, member.UID,
		members.AdminAddContactRequest{
			Type: models.UserContactTypePhone, Value: "+15557654321",
		})
	r.ErrorIs(err, members.ErrContactAlreadyExists)

	// The verified stamp must survive untouched: the member stays pageable.
	stored, err := fx.dbSvc.GetUserContact(ctx, contact.UID)
	r.NoError(err)
	r.NotNil(stored.VerifiedAt, "an admin add must never de-verify an existing contact")
	r.WithinDuration(verifiedAt, *stored.VerifiedAt, time.Second)
}

// TestAdminContactTypeAllowList: only the types with a verification round-trip
// may be provisioned. Anything else would be immediately pageable.
func TestAdminContactTypeAllowList(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMemberFixture(ctx, t, "provision-types")

	_, member := fx.addMember(ctx, t, "colleague@acme.test", "Colleague")

	for _, contactType := range []string{
		models.UserContactTypeEmail,
		models.UserContactTypeTelegram,
		models.UserContactTypeSlackUser,
		models.UserContactTypeWebPush,
		"nonsense",
	} {
		_, err := fx.svc.AddMemberContact(ctx, fx.org.Slug, member.UID,
			members.AdminAddContactRequest{Type: contactType, Value: "+15551230000"})
		r.ErrorIs(err, members.ErrContactTypeNotProvisionable, "type %s must be refused", contactType)
	}

	// Positive control: the two allowed types do go through.
	for i, contactType := range []string{
		models.UserContactTypePhone,
		models.UserContactTypeWhatsApp,
	} {
		created, err := fx.svc.AddMemberContact(ctx, fx.org.Slug, member.UID,
			members.AdminAddContactRequest{
				Type:  contactType,
				Value: []string{"+15551230001", "+15551230002"}[i],
			})
		r.NoError(err)
		r.False(created.Verified)
	}
}

// TestAdminContactRejectsMalformedNumber keeps a typo out of the paging table.
func TestAdminContactRejectsMalformedNumber(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMemberFixture(ctx, t, "provision-e164")

	_, member := fx.addMember(ctx, t, "colleague@acme.test", "Colleague")

	_, err := fx.svc.AddMemberContact(ctx, fx.org.Slug, member.UID,
		members.AdminAddContactRequest{Type: models.UserContactTypePhone, Value: "0612345678"})
	r.ErrorIs(err, members.ErrInvalidContactValue)
}

// TestSendPagingNudgeEmailsTheMember checks the nudge reaches the member and
// carries a link but no verification shortcut.
func TestSendPagingNudgeEmailsTheMember(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMemberFixture(ctx, t, "nudge-send")

	_, member := fx.addMember(ctx, t, "colleague@acme.test", "Colleague")

	r.NoError(fx.svc.SendPagingNudge(ctx, fx.org.Slug, member.UID))
	r.Equal(1, fx.mailer.sent)
	r.Equal([]string{"colleague@acme.test"}, fx.mailer.last.Recipients.To)
	r.Contains(fx.mailer.last.Text, "/dash0/orgs/"+fx.org.Slug+"/account/notifications")
}

// TestSendPagingNudgeWithoutEmailSender surfaces a clear error rather than
// silently pretending it worked.
func TestSendPagingNudgeWithoutEmailSender(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMemberFixture(ctx, t, "nudge-noemail")

	_, member := fx.addMember(ctx, t, "colleague@acme.test", "Colleague")

	bare := members.NewService(fx.dbSvc)
	r.ErrorIs(bare.SendPagingNudge(ctx, fx.org.Slug, member.UID), members.ErrEmailSenderNotConfigured)
}

// TestProvisioningRejectsForeignMember: a member UID from another org must not
// be addressable through this org's route.
func TestProvisioningRejectsForeignMember(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMemberFixture(ctx, t, "provision-foreign")

	other := models.NewOrganization("provision-foreign-2", "Other Org")
	r.NoError(fx.dbSvc.CreateOrganization(ctx, other))

	user := models.NewUser("outsider@acme.test")
	r.NoError(fx.dbSvc.CreateUser(ctx, user))

	foreign := models.NewOrganizationMember(other.UID, user.UID, models.MemberRoleUser)
	r.NoError(fx.dbSvc.CreateOrganizationMember(ctx, foreign))

	_, err := fx.svc.AddMemberContact(ctx, fx.org.Slug, foreign.UID,
		members.AdminAddContactRequest{Type: models.UserContactTypePhone, Value: "+15551239999"})
	r.ErrorIs(err, members.ErrMemberNotFound)

	r.ErrorIs(fx.svc.SendPagingNudge(ctx, fx.org.Slug, foreign.UID), members.ErrMemberNotFound)
}

// mustJSON marshals v, failing the test on error.
func mustJSON(t *testing.T, v any) string {
	t.Helper()

	data, err := json.Marshal(v)
	require.NoError(t, err)

	return string(data)
}
