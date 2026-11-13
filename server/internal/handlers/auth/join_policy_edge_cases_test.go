package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/defaults"
)

const (
	newcomerEmail = "newcomer@acme.com"
	slackTeamID   = "T-ACME"
)

// joinWorld is an organization that already has its owner, and a newcomer
// with a verified address who signs in through an identity provider.
type joinWorld struct {
	*edgeWorld

	org      *models.Organization
	owner    *models.User
	newcomer *models.User
}

func newJoinWorld(t *testing.T) *joinWorld {
	t.Helper()

	w := newEdgeWorld(t)
	slug, owner, _ := w.signedIn(t, models.MemberRoleOwner)

	newcomer := models.NewUser(newcomerEmail)
	now := time.Now()
	newcomer.EmailVerifiedAt = &now
	require.NoError(t, w.dbSvc.CreateUser(w.ctx, newcomer))

	return &joinWorld{edgeWorld: w, org: w.org(t, slug), owner: owner, newcomer: newcomer}
}

func (w *joinWorld) join(opts ...LoginOption) (*models.OrganizationMember, bool, error) {
	return w.svc.JoinOrgViaLogin(w.ctx, w.org, w.newcomer, opts...)
}

// membership returns the newcomer's membership of the organization, or nil.
func (w *joinWorld) membership(t *testing.T) *models.OrganizationMember {
	t.Helper()

	members, err := w.dbSvc.ListMembersByOrg(w.ctx, w.org.UID)
	require.NoError(t, err)

	for _, member := range members {
		if member.UserUID == w.newcomer.UID {
			return member
		}
	}

	return nil
}

// request returns the newcomer's membership request, or nil.
func (w *joinWorld) request(t *testing.T) *models.MembershipRequest {
	t.Helper()

	requests, err := w.dbSvc.ListMembershipRequests(w.ctx, models.ListMembershipRequestsFilter{
		OrganizationUID: w.org.UID,
	})
	require.NoError(t, err)

	for _, request := range requests {
		if request.UserUID == w.newcomer.UID {
			return request
		}
	}

	return nil
}

func (w *joinWorld) invite(t *testing.T) {
	t.Helper()

	_, err := w.svc.CreateInvitation(w.ctx, w.org.Slug, w.owner.UID, InviteRequest{
		Email: newcomerEmail, Role: string(models.MemberRoleAdmin), ExpiresIn: "24h", App: "dash0",
	})
	require.NoError(t, err)
}

func (w *joinWorld) linkSlackWorkspace(t *testing.T) {
	t.Helper()

	require.NoError(t, w.dbSvc.CreateOrganizationProvider(w.ctx,
		models.NewOrganizationProvider(w.org.UID, models.ProviderTypeSlack, slackTeamID)))
}

func (w *joinWorld) setParam(t *testing.T, key string, value any) {
	t.Helper()

	require.NoError(t, w.dbSvc.SetOrgParameter(w.ctx, w.org.UID, key, value, false))
}

// TestJoinViaLoginFallsBackToARequest: an admission rule whose data is
// unusable or cannot be read admits nobody. The login is not failed: the
// newcomer ends up with a membership request instead.
func TestJoinViaLoginFallsBackToARequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare func(t *testing.T, w *joinWorld)
		opts    []LoginOption
		fault   *sqlFault
	}{
		{
			name:    "invitations cannot be listed",
			prepare: func(t *testing.T, w *joinWorld) { t.Helper(); w.invite(t) },
			fault:   &sqlFault{fragments: []string{`FROM "state_entries"`}},
		},
		{
			name: "pattern cannot be read",
			prepare: func(t *testing.T, w *joinWorld) {
				t.Helper()
				w.setParam(t, registrationEmailPatternKey, autoJoinPattern)
			},
			fault: &sqlFault{fragments: []string{`FROM "parameters"`, registrationEmailPatternKey}},
		},
		{
			name: "pattern is not a string",
			prepare: func(t *testing.T, w *joinWorld) {
				t.Helper()
				w.setParam(t, registrationEmailPatternKey, 42)
			},
		},
		{
			name: "Slack workspace auto-join switched off",
			prepare: func(t *testing.T, w *joinWorld) {
				t.Helper()
				w.linkSlackWorkspace(t)
				w.setParam(t, registrationSlackAutoJoinKey, false)
			},
			opts: []LoginOption{WithSlackWorkspace(slackTeamID)},
		},
		{
			name: "Slack workspace auto-join switch unreadable",
			prepare: func(t *testing.T, w *joinWorld) {
				t.Helper()
				w.linkSlackWorkspace(t)
				w.setParam(t, registrationSlackAutoJoinKey, "disabled")
			},
			opts: []LoginOption{WithSlackWorkspace(slackTeamID)},
		},
		{
			name:    "Slack workspace link cannot be read",
			prepare: func(t *testing.T, w *joinWorld) { t.Helper(); w.linkSlackWorkspace(t) },
			opts:    []LoginOption{WithSlackWorkspace(slackTeamID)},
			fault:   &sqlFault{fragments: []string{`FROM "organization_providers"`}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := newJoinWorld(t)
			tt.prepare(t, w)

			var (
				member  *models.OrganizationMember
				pending bool
				err     error
			)

			join := func() { member, pending, err = w.join(tt.opts...) }
			if tt.fault != nil {
				w.fault.during(t, *tt.fault, join)
			} else {
				join()
			}

			require.NoError(t, err)
			require.True(t, pending)
			require.Nil(t, member)
			require.Nil(t, w.membership(t))
			require.Equal(t, models.MembershipRequestStatusPending, w.request(t).Status)
		})
	}
}

// TestJoinViaLoginAdmissions are the controls of the fallback test, plus the
// admissions that survive a degraded store.
func TestJoinViaLoginAdmissions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		prepare  func(t *testing.T, w *joinWorld)
		opts     []LoginOption
		fault    *sqlFault
		wantRole models.MemberRole
	}{
		{
			name:     "invitation",
			prepare:  func(t *testing.T, w *joinWorld) { t.Helper(); w.invite(t) },
			wantRole: models.MemberRoleAdmin,
		},
		{
			name: "invitation without a role, behind entries that are not for the newcomer",
			prepare: func(t *testing.T, w *joinWorld) {
				t.Helper()

				// Keys sort before the newcomer's, so both are walked first.
				require.NoError(t, w.dbSvc.SetStateEntry(w.ctx, &w.org.UID, inviteKeyPrefix+"0-no-value", nil, nil))
				require.NoError(t, w.dbSvc.SetStateEntry(w.ctx, &w.org.UID, inviteKeyPrefix+"1-other",
					&models.JSONMap{keyEmail: "bob@acme.com", "role": string(models.MemberRoleAdmin)}, nil))
				require.NoError(t, w.dbSvc.SetStateEntry(w.ctx, &w.org.UID, inviteKeyPrefix+"2-newcomer",
					&models.JSONMap{keyEmail: "Newcomer@Acme.com"}, nil))
			},
			wantRole: models.MemberRoleUser,
		},
		{
			name: "pattern",
			prepare: func(t *testing.T, w *joinWorld) {
				t.Helper()
				w.setParam(t, registrationEmailPatternKey, autoJoinPattern)
			},
			wantRole: models.MemberRoleUser,
		},
		{
			name:     "Slack workspace",
			prepare:  func(t *testing.T, w *joinWorld) { t.Helper(); w.linkSlackWorkspace(t) },
			opts:     []LoginOption{WithSlackWorkspace(slackTeamID)},
			wantRole: models.MemberRoleUser,
		},
		{
			// A read error keeps the default: one hiccup must not lock a
			// whole workspace out.
			name: "Slack workspace, switch cannot be read",
			prepare: func(t *testing.T, w *joinWorld) {
				t.Helper()
				w.linkSlackWorkspace(t)
				w.setParam(t, registrationSlackAutoJoinKey, false)
			},
			opts:     []LoginOption{WithSlackWorkspace(slackTeamID)},
			fault:    &sqlFault{fragments: []string{`FROM "parameters"`, registrationSlackAutoJoinKey}},
			wantRole: models.MemberRoleUser,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := newJoinWorld(t)
			tt.prepare(t, w)

			var (
				member  *models.OrganizationMember
				pending bool
				err     error
			)

			join := func() { member, pending, err = w.join(tt.opts...) }
			if tt.fault != nil {
				w.fault.during(t, *tt.fault, join)
			} else {
				join()
			}

			require.NoError(t, err)
			require.False(t, pending)
			require.Equal(t, tt.wantRole, member.Role)
			require.Equal(t, tt.wantRole, w.membership(t).Role)
			require.Nil(t, w.request(t))
		})
	}
}

// TestJoinViaLoginSurfacesStoreFailures: when the store fails on a step the
// decision depends on, the login fails. It neither admits nor files a request.
func TestJoinViaLoginSurfacesStoreFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		invited bool
		fault   sqlFault
		wantMsg string
	}{
		{"reading the membership", false, failingSQL(`FROM "organization_members"`), "failed to get member"},
		{"listing the members", false, failingSQL(`FROM "organization_members"`).after(1), "failed to list members"},
		{
			"creating the invited membership", true, failingSQL(`INSERT INTO "organization_members"`),
			"failed to create member",
		},
		{
			"reading the membership request", false, failingSQL(`FROM "membership_requests"`),
			"failed to look up membership request",
		},
		{
			"creating the membership request", false, failingSQL(`INSERT INTO "membership_requests"`),
			"failed to create membership request",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := newJoinWorld(t)
			if tt.invited {
				w.invite(t)
			}

			var (
				member  *models.OrganizationMember
				pending bool
				err     error
			)

			w.fault.during(t, tt.fault, func() { member, pending, err = w.join() })
			require.ErrorIs(t, err, context.Canceled)
			require.ErrorContains(t, err, tt.wantMsg)
			require.Nil(t, member)
			require.False(t, pending)
			require.Nil(t, w.membership(t))
			require.Nil(t, w.request(t))

			// The invitation was not consumed by the failed attempt.
			if tt.invited {
				member, _, err = w.join()
				require.NoError(t, err)
				require.Equal(t, models.MemberRoleAdmin, member.Role)
			}
		})
	}
}

// TestJoinViaLoginWithAnEarlierRequest: a login never overrides an admin's
// decision. Only a request the user canceled is opened again.
func TestJoinViaLoginWithAnEarlierRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status models.MembershipRequestStatus
		want   models.MembershipRequestStatus
	}{
		{models.MembershipRequestStatusPending, models.MembershipRequestStatusPending},
		{models.MembershipRequestStatusRejected, models.MembershipRequestStatusRejected},
		{models.MembershipRequestStatusApproved, models.MembershipRequestStatusApproved},
		{models.MembershipRequestStatusCancelled, models.MembershipRequestStatusPending},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			t.Parallel()

			w := newJoinWorld(t)

			reason := "decided earlier"
			decidedAt := time.Now().Add(-time.Hour)
			earlier := models.NewMembershipRequest(w.org.UID, w.newcomer.UID, nil)
			earlier.Status = tt.status
			earlier.DecisionReason = &reason
			earlier.DecidedAt = &decidedAt
			earlier.DecidedByUID = &w.owner.UID
			require.NoError(t, w.dbSvc.CreateMembershipRequest(w.ctx, earlier))

			member, pending, err := w.join()
			require.NoError(t, err)
			require.True(t, pending)
			require.Nil(t, member)

			stored := w.request(t)
			require.Equal(t, earlier.UID, stored.UID)
			require.Equal(t, tt.want, stored.Status)

			reopened := tt.status == models.MembershipRequestStatusCancelled
			require.Equal(t, reopened, stored.DecidedAt == nil, "only a reopened request loses its decision")
			require.Equal(t, reopened, stored.DecisionReason == nil)
		})
	}

	t.Run("a canceled request that cannot be reopened fails the login", func(t *testing.T) {
		t.Parallel()

		w := newJoinWorld(t)
		earlier := models.NewMembershipRequest(w.org.UID, w.newcomer.UID, nil)
		earlier.Status = models.MembershipRequestStatusCancelled
		require.NoError(t, w.dbSvc.CreateMembershipRequest(w.ctx, earlier))

		var err error

		w.fault.during(t, failingSQL(`UPDATE "membership_requests"`), func() { _, _, err = w.join() })
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorContains(t, err, "failed to reopen membership request")
		require.Equal(t, models.MembershipRequestStatusCancelled, w.request(t).Status)
	})
}

// TestPendingLoginSurvivesAFailedActivityStamp: the org-less session of a
// login that was not admitted does not depend on the last-activity write.
func TestPendingLoginSurvivesAFailedActivityStamp(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newJoinWorld(t)

	var (
		result *ProviderLoginResult
		err    error
	)

	w.fault.during(t, failingSQL(`UPDATE "users"`, "last_active_at"), func() {
		result, err = w.svc.CompleteOrgLogin(w.ctx, w.org, w.newcomer)
	})
	r.NoError(err)
	r.True(result.Pending)
	r.Equal(w.org.Slug, result.PendingOrgSlug)
	r.NotEmpty(result.AccessToken)
	r.Empty(result.RefreshToken, "a pending login gets no session row")

	claims, err := w.svc.ValidateToken(w.ctx, result.AccessToken)
	r.NoError(err)
	r.Empty(claims.OrgSlug)
	r.Empty(claims.Role)
}

func TestLoginJoinRequestSuppressedReason(t *testing.T) {
	t.Parallel()

	saas := &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModeSaaS}}
	selfHosted := &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModeSelfHosted}}
	demoSlug := selfHosted.Demo.ResolvedOrgSlug()

	tests := []struct {
		name string
		cfg  *config.Config
		slug string
		want string
	}{
		{"no configuration", nil, defaults.Organization, ""},
		{"demo organization", selfHosted, demoSlug, suppressReasonDemoOrg},
		{"default organization on SaaS", saas, defaults.Organization, suppressReasonSaaSDefault},
		{"default organization when self-hosted", selfHosted, defaults.Organization, ""},
		{"any other organization on SaaS", saas, "acme", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := &Service{fullCfg: tt.cfg}
			require.Equal(t, tt.want, svc.loginJoinRequestSuppressedReason(models.NewOrganization(tt.slug, "Acme")))
		})
	}
}

// TestParseParamBoolAcceptsANativeInt completes TestParseParamBool: a value
// set in process (not decoded from JSON) may be an int.
func TestParseParamBoolAcceptsANativeInt(t *testing.T) {
	t.Parallel()

	for raw, want := range map[int]bool{0: false, 1: true, 2: true} {
		value, decoded := parseParamBool(raw)
		require.True(t, decoded, "%d", raw)
		require.Equal(t, want, value, "%d", raw)
	}
}
