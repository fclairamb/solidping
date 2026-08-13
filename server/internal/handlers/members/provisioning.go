package members

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/integrations/twilio"
)

// Admin pre-provisioning errors.
var (
	// ErrContactTypeNotProvisionable is returned for any contact type an admin
	// may not create on someone else's behalf. Only the types that go through
	// the code round-trip (phone, WhatsApp) qualify: for every other type
	// "created by an admin" would mean "immediately pageable", which is exactly
	// the wrong-number / harassment vector this feature must not open.
	ErrContactTypeNotProvisionable = errors.New(
		"only phone and whatsapp contacts can be set up for another member")
	// ErrInvalidContactValue is returned when the number is not E.164.
	ErrInvalidContactValue = errors.New("number must be in E.164 format (e.g. +15551234567)")
	// ErrContactAlreadyExists is returned when the member already has this
	// exact contact. Refusing keeps the admin path incapable of touching an
	// existing (possibly verified) row.
	ErrContactAlreadyExists = errors.New("this member already has that contact")
	// ErrEmailSenderNotConfigured is returned when a nudge is requested on an
	// instance with no outbound email.
	ErrEmailSenderNotConfigured = errors.New("email sending is not configured on this server")
)

// Option customizes a members Service at construction.
type Option func(*Service)

// WithEmailSender supplies the outbound email sender used by the paging nudge.
func WithEmailSender(sender email.Sender) Option {
	return func(s *Service) { s.email = sender }
}

// WithAppBaseURL supplies the base URL used to build dashboard links in emails.
func WithAppBaseURL(baseURL string) Option {
	return func(s *Service) { s.appBaseURL = strings.TrimRight(baseURL, "/") }
}

// AdminAddContactRequest is the body of the admin pre-provisioning endpoint.
type AdminAddContactRequest struct {
	Type  string `json:"type"`
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

// AdminContactResponse is the created contact, with no value echoed back beyond
// what the admin just typed and — critically — an explicit verified flag that
// is always false.
type AdminContactResponse struct {
	UID      string `json:"uid"`
	UserUID  string `json:"userUid"`
	Type     string `json:"type"`
	Label    string `json:"label,omitempty"`
	Verified bool   `json:"verified"`
}

// AddMemberContact lets an admin pre-provision a paging contact for another
// member, in UNVERIFIED state.
//
// The invariant this function exists to guarantee: **an admin can never create
// or flip a contact to verified.** It is enforced three ways, not one — the
// type allow-list only admits types that require a code round-trip, a
// pre-existing live contact is refused outright rather than updated, and the
// freshly written row has its verified stamp cleared unconditionally (which
// also covers the restore-a-soft-deleted-row path). The member becomes pageable
// on this contact only after completing the normal verification flow themselves.
func (s *Service) AddMemberContact(
	ctx context.Context, orgSlug, memberUID string, req AdminAddContactRequest,
) (*AdminContactResponse, error) {
	org, member, err := s.resolveMember(ctx, orgSlug, memberUID)
	if err != nil {
		return nil, err
	}

	contactType := strings.TrimSpace(req.Type)
	if !models.ContactRequiresVerification(contactType) {
		return nil, ErrContactTypeNotProvisionable
	}

	value := strings.TrimSpace(req.Value)
	if !twilio.ValidE164(value) {
		return nil, ErrInvalidContactValue
	}

	// The duplicate guard MUST be a type/value lookup, not a route-joined one.
	// A live contact with no route (the member deleted the route, or the row
	// predates routing) is invisible to ListUserContactsWithRoutes, so a
	// route-joined guard would fall through to the upsert below — which matches
	// the (user, org, type, value) unique key and lands on that very row, and
	// the unconditional verified-stamp clear would then silently DE-VERIFY a
	// number the member had already verified, quietly making them unpageable.
	existing, err := s.findLiveContact(ctx, member.UserUID, org.UID, contactType, value)
	if err != nil {
		return nil, err
	}

	if existing != nil {
		return nil, ErrContactAlreadyExists
	}

	contact := models.NewUserContact(member.UserUID, org.UID, contactType, value, req.Label)
	// VerifiedAt is left nil deliberately — see the doc comment.
	if upsertErr := s.db.UpsertUserContact(ctx, contact); upsertErr != nil {
		return nil, fmt.Errorf("create member contact: %w", upsertErr)
	}

	stored, err := s.findContact(ctx, member.UserUID, org.UID, contactType, value)
	if err != nil {
		return nil, err
	}

	// Belt and braces for the restore path: the upsert un-deletes a matching
	// soft-deleted row, which could have carried an old verified stamp. An
	// admin-provisioned contact is never pageable until its owner re-verifies.
	if stored.VerifiedAt != nil {
		if clearErr := s.db.ClearUserContactVerified(ctx, stored.UID); clearErr != nil {
			return nil, fmt.Errorf("clear verified state: %w", clearErr)
		}

		stored.VerifiedAt = nil
	}

	if routeErr := s.db.EnsureUserNotificationRoute(ctx, member.UserUID, org.UID, stored.UID); routeErr != nil {
		return nil, fmt.Errorf("create member route: %w", routeErr)
	}

	return &AdminContactResponse{
		UID:      stored.UID,
		UserUID:  member.UserUID,
		Type:     stored.Type,
		Label:    stored.Label,
		Verified: false,
	}, nil
}

// findLiveContact returns this member's live contact with the given type and
// value, or nil when they have none.
//
// It deliberately reads the type/value index rather than the route-joined view:
// routing is a separate, user-editable concern, so "has a route" is not the same
// question as "this row exists". Everything that must not silently write over an
// existing contact has to ask it this way.
func (s *Service) findLiveContact(
	ctx context.Context, userUID, orgUID, contactType, value string,
) (*models.UserContact, error) {
	// The index is org-agnostic (a value can legitimately repeat across orgs and
	// users), so filter it down to this member in this org.
	contacts, err := s.db.ListUserContactsByTypeValue(ctx, contactType, value)
	if err != nil {
		return nil, fmt.Errorf("look up member contact: %w", err)
	}

	for _, contact := range contacts {
		if contact.UserUID == userUID && contact.OrganizationUID == orgUID {
			return contact, nil
		}
	}

	return nil, nil //nolint:nilnil // "no such contact" is a normal, non-error outcome.
}

// findContact reloads the contact the upsert just wrote or restored.
func (s *Service) findContact(
	ctx context.Context, userUID, orgUID, contactType, value string,
) (*models.UserContact, error) {
	contact, err := s.findLiveContact(ctx, userUID, orgUID, contactType, value)
	if err != nil {
		return nil, err
	}

	if contact == nil {
		return nil, ErrContactNotFoundAfterCreate
	}

	return contact, nil
}

// ErrContactNotFoundAfterCreate is returned when the just-written contact
// cannot be read back — a bug, not a user error.
var ErrContactNotFoundAfterCreate = errors.New("contact not found after creation")

// SendPagingNudge emails a member asking them to finish setting up their alert
// notifications. Deliberately contains no contact data and no verification code
// — it is a nudge with a link, not a shortcut around verification.
func (s *Service) SendPagingNudge(ctx context.Context, orgSlug, memberUID string) error {
	org, member, err := s.resolveMember(ctx, orgSlug, memberUID)
	if err != nil {
		return err
	}

	if s.email == nil {
		return ErrEmailSenderNotConfigured
	}

	user, err := s.db.GetUser(ctx, member.UserUID)
	if err != nil {
		return ErrUserNotFound
	}

	link := s.appBaseURL + "/dash0/orgs/" + org.Slug + "/account/notifications"
	subject := "Set up your alert notifications for " + org.Name

	text := "Hi,\n\n" +
		"An administrator of " + org.Name + " asked you to set up how SolidPing should " +
		"reach you when something breaks.\n\n" +
		"Add and verify a phone or messaging contact here:\n" + link + "\n\n" +
		"Until you verify a contact, alerts can only reach you by email.\n"

	html := "<p>Hi,</p><p>An administrator of <strong>" + org.Name + "</strong> asked you to set up " +
		"how SolidPing should reach you when something breaks.</p>" +
		`<p><a href="` + link + `">Add and verify a phone or messaging contact</a></p>` +
		"<p>Until you verify a contact, alerts can only reach you by email.</p>"

	if _, err := s.email.Send(ctx, &email.Message{
		Recipients: email.Recipients{To: []string{user.Email}},
		Subject:    subject,
		HTML:       html,
		Text:       text,
	}); err != nil {
		return fmt.Errorf("send paging nudge: %w", err)
	}

	return nil
}

// resolveMember loads the org and asserts the membership belongs to it.
func (s *Service) resolveMember(
	ctx context.Context, orgSlug, memberUID string,
) (*models.Organization, *models.OrganizationMember, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrOrganizationNotFound
		}

		return nil, nil, err
	}

	member, err := s.db.GetOrganizationMember(ctx, memberUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrMemberNotFound
		}

		return nil, nil, err
	}

	if member == nil || member.OrganizationUID != org.UID {
		return nil, nil, ErrMemberNotFound
	}

	return org, member, nil
}
