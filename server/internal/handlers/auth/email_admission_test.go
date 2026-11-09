package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Email-based admission (an invitation matched by address, an email-domain
// auto-join pattern) trusts the address only once it is verified. An email
// change clears the verification, so a changed address admits nobody.

const (
	admissionEmail   = "bob@acme.com"
	admissionPattern = `@acme\.com$`
)

// admissionRun runs one email-based admission path for user; org is the org
// the path is meant to admit the user to.
type admissionRun func(
	ctx context.Context, t *testing.T, f *personalOrgFixture, org *models.Organization, user *models.User,
)

type admissionPath struct {
	name string
	// invitation: the path matches an invitation (else an email pattern).
	invitation bool
	run        admissionRun
}

func admissionPaths() []admissionPath {
	var joinViaLogin admissionRun = func(
		ctx context.Context, t *testing.T, f *personalOrgFixture, org *models.Organization, user *models.User,
	) {
		t.Helper()

		_, _, err := f.svc.JoinOrgViaLogin(ctx, org, user)
		require.NoError(t, err)
	}

	var ensurePersonal admissionRun = func(
		ctx context.Context, t *testing.T, f *personalOrgFixture, _ *models.Organization, user *models.User,
	) {
		t.Helper()

		f.svc.ensurePersonalOrg(ctx, user)
	}

	return []admissionPath{
		{name: "login on the org: invitation", invitation: true, run: joinViaLogin},
		{name: "login on the org: email pattern", run: joinViaLogin},
		{name: "org-less sign-in: invitation", invitation: true, run: ensurePersonal},
		{name: "org-less sign-in: email pattern", run: ensurePersonal},
		{
			name: "auto-join of other orgs: email pattern",
			run: func(ctx context.Context, t *testing.T, f *personalOrgFixture, _ *models.Organization, user *models.User) {
				t.Helper()

				f.svc.autoJoinMatchingOrgs(ctx, user)
			},
		},
	}
}

// liveInvitations counts the org's outstanding invitations.
func liveInvitations(ctx context.Context, t *testing.T, f *personalOrgFixture, org *models.Organization) int {
	t.Helper()

	list, err := f.svc.ListInvitations(ctx, org.Slug)
	require.NoError(t, err)

	return len(list.Data)
}

// TestEmailAdmissionRequiresVerifiedEmail: every email-based admission path
// admits a verified address (positive control) and refuses the same address
// unverified, leaving the invitation for its real recipient.
func TestEmailAdmissionRequiresVerifiedEmail(t *testing.T) {
	t.Parallel()

	for _, path := range admissionPaths() {
		for _, verified := range []bool{true, false} {
			name := path.name + "/unverified"
			if verified {
				name = path.name + "/verified"
			}

			t.Run(name, func(t *testing.T) {
				t.Parallel()

				r := require.New(t)
				f, ctx := newPersonalOrgFixture(t)

				org, inviter := f.seededOrg(ctx, t, "target", "boss@target.example")
				if path.invitation {
					_, err := f.svc.CreateInvitation(ctx, org.Slug, inviter.UID, InviteRequest{
						Email: admissionEmail, Role: string(models.MemberRoleAdmin), ExpiresIn: "24h", App: "dash0",
					})
					r.NoError(err)
				} else {
					r.NoError(f.db.SetOrgParameter(ctx, org.UID, "registration.email_pattern", admissionPattern, false))
				}

				user := f.passwordUser(ctx, t, admissionEmail, "Bob")
				if !verified {
					r.NoError(f.db.UpdateUser(ctx, user.UID, &models.UserUpdate{ClearEmailVerifiedAt: true}))
					user.EmailVerifiedAt = nil
				}

				path.run(ctx, t, f, org, user)

				member, err := f.db.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
				if !verified {
					r.Error(err, "an unverified email must not admit the user")

					if path.invitation {
						r.Equal(1, liveInvitations(ctx, t, f, org), "the invitation must stay for its recipient")
					}

					return
				}

				r.NoError(err, "a verified email is admitted")

				if path.invitation {
					r.Equal(models.MemberRoleAdmin, member.Role)
					r.Zero(liveInvitations(ctx, t, f, org), "the invitation is consumed")
				} else {
					r.Equal(models.MemberRoleUser, member.Role)
				}
			})
		}
	}
}

// TestChangedEmailAdmitsNobodyUntilVerified walks the whole flow: a user
// changes their email through PATCH /auth/me to an address an org invited
// and another org's pattern matches, then signs in through a federated
// login. Nothing admits them; once the address is verified (positive
// control), the same sign-in does.
func TestChangedEmailAdmitsNobodyUntilVerified(t *testing.T) {
	t.Parallel()

	for _, verifiedAfterChange := range []bool{false, true} {
		name := "changed email stays out"
		if verifiedAfterChange {
			name = "verified email is admitted"
		}

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			f, ctx := newEmailChangeFixture(t, "mallory@elsewhere.example")
			pf := &personalOrgFixture{loginAuditFixture: &loginAuditFixture{svc: f.svc, db: f.db}}

			invitingOrg, inviter := pf.seededOrg(ctx, t, "inviting", "boss@inviting.example")
			_, err := f.svc.CreateInvitation(ctx, invitingOrg.Slug, inviter.UID, InviteRequest{
				Email: admissionEmail, Role: string(models.MemberRoleAdmin), ExpiresIn: "24h", App: "dash0",
			})
			r.NoError(err)

			patternOrg, _ := pf.seededOrg(ctx, t, "patterned", "boss@patterned.example")
			r.NoError(f.db.SetOrgParameter(ctx, patternOrg.UID, "registration.email_pattern", admissionPattern, false))

			rec := patchMe(t, ctx, f.handler, f.claimsA,
				`{"email":"`+admissionEmail+`","currentPassword":"`+emailChangePassword+`"}`)
			r.Equal(http.StatusOK, rec.Code, rec.Body.String())

			if verifiedAfterChange {
				verifiedAt := time.Now()
				r.NoError(f.db.UpdateUser(ctx, f.user.UID, &models.UserUpdate{EmailVerifiedAt: &verifiedAt}))
			}

			user, err := f.db.GetUser(ctx, f.user.UID)
			r.NoError(err)
			r.Equal(admissionEmail, user.Email)

			_, err = f.svc.CompleteOrgLogin(ctx, invitingOrg, user, WithLoginMethod(signupMethodGoogle))
			r.NoError(err)

			_, invitedErr := f.db.GetMemberByUserAndOrg(ctx, user.UID, invitingOrg.UID)
			_, patternErr := f.db.GetMemberByUserAndOrg(ctx, user.UID, patternOrg.UID)

			if verifiedAfterChange {
				r.NoError(invitedErr, "a verified address uses its invitation")
				r.NoError(patternErr, "a verified address matches the pattern")
				r.Zero(liveInvitations(ctx, t, pf, invitingOrg))

				return
			}

			r.Error(invitedErr, "a changed, unverified address must not use the invitation")
			r.Error(patternErr, "a changed, unverified address must not match the pattern")
			r.Equal(1, liveInvitations(ctx, t, pf, invitingOrg), "the invitation must stay for its recipient")
		})
	}
}
