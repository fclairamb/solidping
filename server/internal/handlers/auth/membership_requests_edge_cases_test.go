package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// requestWorld is an organization with an admin, a candidate who is not a
// member, and a mail queue to observe the notifications.
type requestWorld struct {
	svc       *Service
	db        db.Service
	ctx       context.Context //nolint:containedctx // test fixture
	fault     *queryFault
	org       *models.Organization
	admin     *models.User
	candidate *models.User
}

func newRequestWorld(t *testing.T) *requestWorld {
	t.Helper()

	cfg := &config.Config{
		Server: config.ServerConfig{BaseURL: "http://127.0.0.1:4000"},
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
	}
	svc, dbSvc, ctx := setupAuthTestServiceWithJobs(t, cfg)

	w := &requestWorld{svc: svc, db: dbSvc, ctx: ctx, fault: installQueryFault(dbSvc)}

	w.org = models.NewOrganization("requests-org", "Requests")
	require.NoError(t, dbSvc.CreateOrganization(ctx, w.org))

	w.admin = w.member(t, "admin@acme.com", models.MemberRoleAdmin)

	w.candidate = models.NewUser("candidate@acme.com")
	require.NoError(t, dbSvc.CreateUser(ctx, w.candidate))

	return w
}

func (w *requestWorld) member(t *testing.T, email string, role models.MemberRole) *models.User {
	t.Helper()

	user := models.NewUser(email)
	require.NoError(t, w.db.CreateUser(w.ctx, user))
	require.NoError(t, w.db.CreateOrganizationMember(w.ctx, models.NewOrganizationMember(w.org.UID, user.UID, role)))

	return user
}

func (w *requestWorld) ask() (*MembershipRequestSummary, error) {
	return w.svc.CreateMembershipRequest(w.ctx, w.candidate.UID, MembershipRequestCreateRequest{
		OrgSlug: w.org.Slug, Message: "please",
	})
}

// seed stores a request of the candidate in the given state.
func (w *requestWorld) seed(
	t *testing.T, status models.MembershipRequestStatus, decidedAt *time.Time,
) *models.MembershipRequest {
	t.Helper()

	request := models.NewMembershipRequest(w.org.UID, w.candidate.UID, nil)
	request.Status = status
	request.DecidedAt = decidedAt
	require.NoError(t, w.db.CreateMembershipRequest(w.ctx, request))

	return request
}

func (w *requestWorld) stored(t *testing.T, uid string) *models.MembershipRequest {
	t.Helper()

	request, err := w.db.GetMembershipRequest(w.ctx, uid)
	require.NoError(t, err)

	return request
}

// mailed returns the recipients of the queued emails.
func (w *requestWorld) mailed(t *testing.T) []string {
	t.Helper()

	jobs, err := w.db.ListJobs(w.ctx, nil, 0)
	require.NoError(t, err)

	out := make([]string, 0, len(jobs))

	for _, job := range jobs {
		to, _ := job.Config["to"].([]any)
		for _, address := range to {
			if recipient, ok := address.(string); ok {
				out = append(out, recipient)
			}
		}
	}

	return out
}

func (w *requestWorld) candidateRole(t *testing.T) models.MemberRole {
	t.Helper()

	member, err := w.db.GetMemberByUserAndOrg(w.ctx, w.candidate.UID, w.org.UID)
	if err != nil {
		return ""
	}

	return member.Role
}

// TestMembershipRequestOverAnEarlierOne: what asking again does depends on
// the state of the earlier request.
func TestMembershipRequestOverAnEarlierOne(t *testing.T) {
	t.Parallel()

	recently := time.Now().Add(-time.Hour)

	tests := []struct {
		name      string
		status    models.MembershipRequestStatus
		decidedAt *time.Time
		want      error
	}{
		{"approved", models.MembershipRequestStatusApproved, &recently, ErrAlreadyAMember},
		{"rejected recently", models.MembershipRequestStatusRejected, &recently, ErrRequestCooldownActive},
		{"rejected with no decision time on record", models.MembershipRequestStatusRejected, nil, nil},
		{"canceled", models.MembershipRequestStatusCancelled, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := newRequestWorld(t)
			earlier := w.seed(t, tt.status, tt.decidedAt)

			summary, err := w.ask()
			if tt.want != nil {
				require.ErrorIs(t, err, tt.want)
				require.Equal(t, tt.status, w.stored(t, earlier.UID).Status)
				require.Empty(t, w.mailed(t))

				return
			}

			require.NoError(t, err)
			require.Equal(t, earlier.UID, summary.UID, "the earlier request is reopened, not duplicated")
			require.Equal(t, models.MembershipRequestStatusPending, w.stored(t, earlier.UID).Status)
			require.Equal(t, []string{w.admin.Email}, w.mailed(t))
		})
	}
}

// TestMembershipRequestSurfacesStoreFailures: a request that could not be
// written is an error, and nobody is notified about it.
func TestMembershipRequestSurfacesStoreFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		canceled bool
		fault    sqlFault
	}{
		{"looking the earlier request up", false, failingSQL(`FROM "membership_requests"`)},
		{"creating the request", false, failingSQL(`INSERT INTO "membership_requests"`)},
		{"reopening a canceled request", true, failingSQL(`UPDATE "membership_requests"`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := newRequestWorld(t)

			var earlier *models.MembershipRequest
			if tt.canceled {
				earlier = w.seed(t, models.MembershipRequestStatusCancelled, nil)
			}

			var (
				summary *MembershipRequestSummary
				err     error
			)

			w.fault.during(t, tt.fault, func() { summary, err = w.ask() })
			require.ErrorIs(t, err, context.Canceled)
			require.Nil(t, summary)
			require.Empty(t, w.mailed(t))

			if earlier != nil {
				require.Equal(t, models.MembershipRequestStatusCancelled, w.stored(t, earlier.UID).Status)
			}

			// Control: the same request goes through once the store is back.
			summary, err = w.ask()
			require.NoError(t, err)
			require.Equal(t, models.MembershipRequestStatusPending, summary.Status)
		})
	}
}

// TestMembershipRequestNotifiesAdminsOnly: the notification goes to the
// members who can decide, and its failure never fails the request.
func TestMembershipRequestNotifiesAdminsOnly(t *testing.T) {
	t.Parallel()

	t.Run("admins and owners, not the others", func(t *testing.T) {
		t.Parallel()

		w := newRequestWorld(t)
		owner := w.member(t, "owner@acme.com", models.MemberRoleOwner)
		w.member(t, "viewer@acme.com", models.MemberRoleViewer)
		w.member(t, "user@acme.com", models.MemberRoleUser)

		_, err := w.ask()
		require.NoError(t, err)
		require.ElementsMatch(t, []string{w.admin.Email, owner.Email}, w.mailed(t))
	})

	failures := map[string]sqlFault{
		"requester cannot be read": failingSQL(`FROM "users"`),
		// The first membership read is the "already a member" check.
		"admins cannot be listed": failingSQL(`FROM "organization_members"`).after(1),
	}
	for name, fault := range failures {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newRequestWorld(t)

			var (
				summary *MembershipRequestSummary
				err     error
			)

			w.fault.during(t, fault, func() { summary, err = w.ask() })
			require.NoError(t, err)
			require.Equal(t, models.MembershipRequestStatusPending, w.stored(t, summary.UID).Status)
			require.Empty(t, w.mailed(t))
		})
	}
}

// TestMembershipRequestDecisions covers approving and rejecting.
func TestMembershipRequestDecisions(t *testing.T) {
	t.Parallel()

	for _, role := range []models.MemberRole{models.MemberRoleAdmin, models.MemberRoleViewer, models.MemberRoleUser} {
		t.Run("approved as "+string(role), func(t *testing.T) {
			t.Parallel()

			w := newRequestWorld(t)
			request := w.seed(t, models.MembershipRequestStatusPending, nil)

			require.NoError(t, w.svc.ApproveMembershipRequest(w.ctx, w.admin.UID, w.org.Slug, request.UID, string(role)))
			require.Equal(t, role, w.candidateRole(t))
			require.Equal(t, models.MembershipRequestStatusApproved, w.stored(t, request.UID).Status)
			require.Equal(t, []string{w.candidate.Email}, w.mailed(t))
		})
	}

	t.Run("an unknown role is approved as a plain user", func(t *testing.T) {
		t.Parallel()

		w := newRequestWorld(t)
		request := w.seed(t, models.MembershipRequestStatusPending, nil)

		require.NoError(t, w.svc.ApproveMembershipRequest(w.ctx, w.admin.UID, w.org.Slug, request.UID, "owner"))
		require.Equal(t, models.MemberRoleUser, w.candidateRole(t))
	})

	t.Run("a request of another organization is not found", func(t *testing.T) {
		t.Parallel()

		w := newRequestWorld(t)
		request := w.seed(t, models.MembershipRequestStatusPending, nil)

		other := models.NewOrganization("other-org", "Other")
		require.NoError(t, w.db.CreateOrganization(w.ctx, other))

		require.ErrorIs(t,
			w.svc.ApproveMembershipRequest(w.ctx, w.admin.UID, other.Slug, request.UID, ""), ErrRequestNotFound)
		require.ErrorIs(t,
			w.svc.RejectMembershipRequest(w.ctx, w.admin.UID, other.Slug, request.UID, "no"), ErrRequestNotFound)
		require.Equal(t, models.MembershipRequestStatusPending, w.stored(t, request.UID).Status)
		require.Empty(t, w.candidateRole(t))
	})

	t.Run("approval that cannot be written leaves the request pending", func(t *testing.T) {
		t.Parallel()

		w := newRequestWorld(t)
		request := w.seed(t, models.MembershipRequestStatusPending, nil)

		var err error

		w.fault.during(t, failingSQL(`INSERT INTO "organization_members"`), func() {
			err = w.svc.ApproveMembershipRequest(w.ctx, w.admin.UID, w.org.Slug, request.UID, "")
		})
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, models.MembershipRequestStatusPending, w.stored(t, request.UID).Status,
			"the status and the membership are written together")
		require.Empty(t, w.candidateRole(t))
		require.Empty(t, w.mailed(t))
	})

	t.Run("rejection that cannot be written leaves the request pending", func(t *testing.T) {
		t.Parallel()

		w := newRequestWorld(t)
		request := w.seed(t, models.MembershipRequestStatusPending, nil)

		var err error

		w.fault.during(t, failingSQL(`UPDATE "membership_requests"`), func() {
			err = w.svc.RejectMembershipRequest(w.ctx, w.admin.UID, w.org.Slug, request.UID, "no")
		})
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, models.MembershipRequestStatusPending, w.stored(t, request.UID).Status)
		require.Empty(t, w.mailed(t))
	})

	t.Run("the decision stands when the requester cannot be notified", func(t *testing.T) {
		t.Parallel()

		w := newRequestWorld(t)
		request := w.seed(t, models.MembershipRequestStatusPending, nil)

		var err error

		w.fault.during(t, failingSQL(`FROM "users"`), func() {
			err = w.svc.RejectMembershipRequest(w.ctx, w.admin.UID, w.org.Slug, request.UID, "not this time")
		})
		require.NoError(t, err)
		require.Empty(t, w.mailed(t))

		own, err := w.svc.ListOwnMembershipRequests(w.ctx, w.candidate.UID)
		require.NoError(t, err)
		require.Len(t, own.Data, 1)
		require.Equal(t, models.MembershipRequestStatusRejected, own.Data[0].Status)
		require.Equal(t, "not this time", own.Data[0].DecisionReason)
	})
}
