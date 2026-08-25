package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestSessionMintingPathsCaptureRequestProvenance is the regression test for
// spec 2026-08-24-02: federated login, registration and invitation
// acceptance all used to mint their session row from a blank auth.Context,
// so properties.created_with never carried the UA/IP the request-meta
// middleware had already parked on ctx (source: the audit trail worked,
// the session row did not).
//
// Each path gets both a positive control (ctx carries request meta, so it
// must land on the row) and a negative control (ctx carries none, so the
// fields must be OMITTED — never written as the empty string "").
func TestSessionMintingPathsCaptureRequestProvenance(t *testing.T) {
	t.Parallel()

	const (
		wantUA = "acme-test-agent/1.0"
		wantIP = "203.0.113.9"
	)

	tests := []struct {
		name string
		mint func(t *testing.T, ctx context.Context, f *loginAuditFixture) *models.User
	}{
		{
			// The bug this spec exists for: every federated connector shares
			// this tail via CompleteOrgLogin.
			name: "federated login through CompleteOrgLogin",
			mint: func(t *testing.T, ctx context.Context, f *loginAuditFixture) *models.User {
				t.Helper()
				r := require.New(t)

				user := f.user(ctx, t, "federated@acme.com")
				_, err := f.svc.CompleteOrgLogin(ctx, f.org, user, WithLoginMethod(signupMethodOIDC))
				r.NoError(err)

				return user
			},
		},
		{
			name: "registration",
			mint: func(t *testing.T, ctx context.Context, f *loginAuditFixture) *models.User {
				t.Helper()
				r := require.New(t)

				f.svc.fullCfg.Auth.RegistrationEmailPattern = ".*"

				const email = "registrant@acme.com"

				// autoJoinMatchingOrgs needs a pattern to admit the new user
				// into an org — otherwise ConfirmRegistration returns an
				// org-less LoginResponse with no session minted at all.
				r.NoError(f.db.SetOrgParameter(ctx, f.org.UID, registrationEmailPatternKey, `@acme\.com$`, false))

				_, err := f.svc.Register(ctx, RegisterRequest{
					Name: "Reg User", Email: email, Password: "supersecret123",
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

				user, err := f.db.GetUserByEmail(ctx, email)
				r.NoError(err)

				return user
			},
		},
		{
			name: "invitation acceptance",
			mint: func(t *testing.T, ctx context.Context, f *loginAuditFixture) *models.User {
				t.Helper()
				r := require.New(t)

				inviter := f.user(ctx, t, "inviter@acme.com")
				resp, err := f.svc.CreateInvitation(ctx, f.org.Slug, inviter.UID, InviteRequest{
					Email: "invitee@acme.com", Role: "user", ExpiresIn: "24h", App: "dash0",
				})
				r.NoError(err)

				_, err = f.svc.AcceptInvite(ctx, AcceptInviteRequest{
					Token: resp.Token, Name: "Invitee", Password: "supersecret123",
				})
				r.NoError(err)

				user, err := f.db.GetUserByEmail(ctx, "invitee@acme.com")
				r.NoError(err)

				return user
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			t.Run("positive control: ctx carries request meta", func(t *testing.T) {
				t.Parallel()

				r := require.New(t)
				f, ctx := newLoginAuditFixture(t)
				ctx = audit.WithRequest(ctx, wantIP, wantUA)

				user := tc.mint(t, ctx, f)

				rows, err := f.db.ListUserTokensByType(ctx, user.UID, models.TokenTypeRefresh)
				r.NoError(err)
				r.Len(rows, 1, "precondition: exactly one session must have been minted")

				createdWith := extractCreatedWith(rows[0].Properties)
				r.NotNil(createdWith, "a session minted with request meta on ctx must record it")
				r.Equal(wantUA, createdWith.UserAgent)
				r.Equal(wantIP, createdWith.RemoteAddr)
			})

			t.Run("negative control: ctx carries no request meta", func(t *testing.T) {
				t.Parallel()

				r := require.New(t)
				f, ctx := newLoginAuditFixture(t)

				user := tc.mint(t, ctx, f)

				rows, err := f.db.ListUserTokensByType(ctx, user.UID, models.TokenTypeRefresh)
				r.NoError(err)
				r.Len(rows, 1, "precondition: exactly one session must have been minted")

				createdWith := extractCreatedWith(rows[0].Properties)
				if createdWith != nil {
					r.Empty(createdWith.UserAgent, "no request meta on ctx must not fabricate a user agent")
					r.Empty(createdWith.RemoteAddr, "no request meta on ctx must not fabricate an IP")
				}

				// The stored map itself must OMIT the keys, never write "".
				raw, ok := rows[0].Properties[keyCreatedWith].(map[string]any)
				r.True(ok, "created_with must still be present (it always carries the method)")

				_, hasUA := raw["userAgent"]
				r.False(hasUA, "userAgent key must be omitted, not written as an empty string")

				_, hasIP := raw["remoteAddr"]
				r.False(hasIP, "remoteAddr key must be omitted, not written as an empty string")
			})
		})
	}
}
