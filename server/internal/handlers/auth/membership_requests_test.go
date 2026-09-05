package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// seedMembershipFixtures creates an admin, an org, and a candidate user
// (no membership yet) for the membership-request flow tests.
func seedMembershipFixtures(
	t *testing.T, slug string,
) (*Service, *models.Organization, *models.User, *models.User) {
	t.Helper()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "http://127.0.0.1:4000")

	org := models.NewOrganization(slug, "Membership Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	admin := models.NewUser("admin-" + slug + "@example.com")
	r.NoError(dbSvc.CreateUser(ctx, admin))
	r.NoError(dbSvc.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(org.UID, admin.UID, models.MemberRoleAdmin),
	))

	candidate := models.NewUser("candidate-" + slug + "@example.com")
	r.NoError(dbSvc.CreateUser(ctx, candidate))

	return svc, org, admin, candidate
}

func TestCreateMembershipRequest(t *testing.T) {
	t.Parallel()

	t.Run("creates pending request for new candidate", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, org, _, candidate := seedMembershipFixtures(t, "mr-create")

		summary, err := svc.CreateMembershipRequest(t.Context(), candidate.UID,
			MembershipRequestCreateRequest{OrgSlug: org.Slug, Message: "let me in"})
		r.NoError(err)
		r.Equal(models.MembershipRequestStatusPending, summary.Status)
		r.Equal(org.Slug, summary.Organization.Slug)
		r.Equal("let me in", summary.Message)
	})

	t.Run("returns ALREADY_A_MEMBER when already in org", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, org, admin, _ := seedMembershipFixtures(t, "mr-already")

		_, err := svc.CreateMembershipRequest(t.Context(), admin.UID,
			MembershipRequestCreateRequest{OrgSlug: org.Slug})
		r.ErrorIs(err, ErrAlreadyAMember)
	})

	t.Run("returns REQUEST_PENDING on duplicate open request", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, org, _, candidate := seedMembershipFixtures(t, "mr-dup")

		_, err := svc.CreateMembershipRequest(t.Context(), candidate.UID,
			MembershipRequestCreateRequest{OrgSlug: org.Slug})
		r.NoError(err)

		_, err = svc.CreateMembershipRequest(t.Context(), candidate.UID,
			MembershipRequestCreateRequest{OrgSlug: org.Slug})
		r.ErrorIs(err, ErrRequestPending)
	})

	t.Run("returns ORGANIZATION_NOT_FOUND on unknown slug", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, _, _, candidate := seedMembershipFixtures(t, "mr-noorg")

		_, err := svc.CreateMembershipRequest(t.Context(), candidate.UID,
			MembershipRequestCreateRequest{OrgSlug: "does-not-exist"})
		r.ErrorIs(err, ErrOrganizationNotFound)
	})
}

func TestCancelMembershipRequest(t *testing.T) {
	t.Parallel()

	t.Run("owner can cancel own request", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, org, _, candidate := seedMembershipFixtures(t, "mr-cancel")

		summary, err := svc.CreateMembershipRequest(t.Context(), candidate.UID,
			MembershipRequestCreateRequest{OrgSlug: org.Slug})
		r.NoError(err)

		r.NoError(svc.CancelMembershipRequest(t.Context(), candidate.UID, summary.UID))
	})

	t.Run("non-owner gets REQUEST_NOT_FOUND", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, org, admin, candidate := seedMembershipFixtures(t, "mr-nonowner")

		summary, err := svc.CreateMembershipRequest(t.Context(), candidate.UID,
			MembershipRequestCreateRequest{OrgSlug: org.Slug})
		r.NoError(err)

		err = svc.CancelMembershipRequest(t.Context(), admin.UID, summary.UID)
		r.ErrorIs(err, ErrRequestNotFound)
	})
}

func TestApproveAndRejectMembershipRequest(t *testing.T) {
	t.Parallel()

	t.Run("approve creates membership and surfaces decision", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, org, admin, candidate := seedMembershipFixtures(t, "mr-approve")

		summary, err := svc.CreateMembershipRequest(t.Context(), candidate.UID,
			MembershipRequestCreateRequest{OrgSlug: org.Slug})
		r.NoError(err)

		r.NoError(svc.ApproveMembershipRequest(
			t.Context(), admin.UID, org.Slug, summary.UID, "user",
		))

		// Membership exists now.
		member, err := svc.db.GetMemberByUserAndOrg(t.Context(), candidate.UID, org.UID)
		r.NoError(err)
		r.Equal(models.MemberRoleUser, member.Role)
	})

	t.Run("reject sets decision reason", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, org, admin, candidate := seedMembershipFixtures(t, "mr-reject")

		summary, err := svc.CreateMembershipRequest(t.Context(), candidate.UID,
			MembershipRequestCreateRequest{OrgSlug: org.Slug})
		r.NoError(err)

		r.NoError(svc.RejectMembershipRequest(
			t.Context(), admin.UID, org.Slug, summary.UID, "spam",
		))

		row, err := svc.db.GetMembershipRequest(t.Context(), summary.UID)
		r.NoError(err)
		r.Equal(models.MembershipRequestStatusRejected, row.Status)
		r.NotNil(row.DecisionReason)
		r.Equal("spam", *row.DecisionReason)
	})

	t.Run("admin can re-approve a rejected row without cooldown", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, org, admin, candidate := seedMembershipFixtures(t, "mr-readmin")

		summary, err := svc.CreateMembershipRequest(t.Context(), candidate.UID,
			MembershipRequestCreateRequest{OrgSlug: org.Slug})
		r.NoError(err)

		r.NoError(svc.RejectMembershipRequest(
			t.Context(), admin.UID, org.Slug, summary.UID, "",
		))

		// Admin override: approving the same row works immediately.
		r.NoError(svc.ApproveMembershipRequest(
			t.Context(), admin.UID, org.Slug, summary.UID, "user",
		))

		_, err = svc.db.GetMemberByUserAndOrg(t.Context(), candidate.UID, org.UID)
		r.NoError(err)
	})
}

func TestMembershipRequestCooldown(t *testing.T) {
	t.Parallel()

	t.Run("rejected requester is in cooldown immediately", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, org, admin, candidate := seedMembershipFixtures(t, "mr-cool")

		summary, err := svc.CreateMembershipRequest(t.Context(), candidate.UID,
			MembershipRequestCreateRequest{OrgSlug: org.Slug})
		r.NoError(err)

		r.NoError(svc.RejectMembershipRequest(
			t.Context(), admin.UID, org.Slug, summary.UID, "",
		))

		_, err = svc.CreateMembershipRequest(t.Context(), candidate.UID,
			MembershipRequestCreateRequest{OrgSlug: org.Slug})
		r.ErrorIs(err, ErrRequestCooldownActive)
	})

	t.Run("rejected requester can re-request once cooldown elapses", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, org, admin, candidate := seedMembershipFixtures(t, "mr-coolaged")

		summary, err := svc.CreateMembershipRequest(t.Context(), candidate.UID,
			MembershipRequestCreateRequest{OrgSlug: org.Slug})
		r.NoError(err)

		r.NoError(svc.RejectMembershipRequest(
			t.Context(), admin.UID, org.Slug, summary.UID, "",
		))

		// Backdate decided_at past the cooldown window.
		row, err := svc.db.GetMembershipRequest(t.Context(), summary.UID)
		r.NoError(err)
		past := time.Now().Add(-(time.Duration(defaultMembershipRequestCooldownDays+1) * 24 * time.Hour))
		row.DecidedAt = &past
		r.NoError(svc.db.UpdateMembershipRequest(t.Context(), row))

		reopened, err := svc.CreateMembershipRequest(t.Context(), candidate.UID,
			MembershipRequestCreateRequest{OrgSlug: org.Slug})
		r.NoError(err)
		r.Equal(models.MembershipRequestStatusPending, reopened.Status)
	})

	t.Run("canceled requester can re-request immediately", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, org, _, candidate := seedMembershipFixtures(t, "mr-recancel")

		summary, err := svc.CreateMembershipRequest(t.Context(), candidate.UID,
			MembershipRequestCreateRequest{OrgSlug: org.Slug})
		r.NoError(err)

		r.NoError(svc.CancelMembershipRequest(t.Context(), candidate.UID, summary.UID))

		reopened, err := svc.CreateMembershipRequest(t.Context(), candidate.UID,
			MembershipRequestCreateRequest{OrgSlug: org.Slug})
		r.NoError(err)
		r.Equal(models.MembershipRequestStatusPending, reopened.Status)
	})
}

func TestAutoJoinMatchingOrgsSkipsBadStoredPattern(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "http://127.0.0.1:4000")

	org := models.NewOrganization("badregex-org", "Bad Regex Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("victim@example.com")
	r.NoError(dbSvc.CreateUser(ctx, user))

	// Stored pattern bypasses validation (mimics legacy data inserted before
	// validateAutoJoinRegex shipped). The defensive guard inside
	// autoJoinMatchingOrgs must skip it instead of crashing the caller and
	// must not adopt the user into the org.
	r.NoError(dbSvc.SetOrgParameter(ctx, org.UID, "registration.email_pattern", ".*", false))

	svc.autoJoinMatchingOrgs(ctx, user.UID, user.Email)

	_, err := dbSvc.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	r.Error(err) // no membership created
}

// TestNotifyAdminsOfMembershipRequest_RequestsURLIsExact pins the URL the
// "New membership request" email hands admins to the CURRENT dash0 route.
// The formatter tests only ever exercise opaque placeholder URLs
// (https://x.test/requests), which is exactly why a route rename
// (members?tab=requests -> organization/requests) went undetected. The
// assertion is on the EXACT path, not Contains("requests"), so a future
// rename fails loudly here instead of silently in an inbox.
//
// Cross-check: web/dash0/src/routes/orgs/$org/organization.requests.tsx
// registers "/orgs/$org/organization/requests" in the dash0 route table
// (routeTree.gen.ts) — keep this string in sync with that route.
func TestNotifyAdminsOfMembershipRequest_RequestsURLIsExact(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbService.Initialize(ctx))
	t.Cleanup(func() { _ = dbService.Close() })

	jobs := jobsvc.NewService(dbService.DB(), dbService, notifier.NewLocalEventNotifier(), nil)

	fullCfg := &config.Config{
		Server: config.ServerConfig{BaseURL: "http://127.0.0.1:4000"},
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
	}

	svc := NewService(dbService, fullCfg.Auth, fullCfg, jobs, nil)

	org := models.NewOrganization("mr-url-org", "URL Org")
	r.NoError(dbService.CreateOrganization(ctx, org))

	admin := models.NewUser("admin-mr-url@example.com")
	r.NoError(dbService.CreateUser(ctx, admin))
	r.NoError(dbService.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(org.UID, admin.UID, models.MemberRoleAdmin),
	))

	candidate := models.NewUser("candidate-mr-url@example.com")
	r.NoError(dbService.CreateUser(ctx, candidate))

	_, err = svc.CreateMembershipRequest(ctx, candidate.UID,
		MembershipRequestCreateRequest{OrgSlug: org.Slug, Message: "let me in"})
	r.NoError(err)

	enqueued, err := dbService.ListJobs(ctx, &org.UID, 0)
	r.NoError(err)
	r.Len(enqueued, 1)
	r.Equal(string(jobdef.JobTypeEmail), enqueued[0].Type)

	templateData, ok := enqueued[0].Config["templateData"].(map[string]any)
	r.True(ok, "templateData must decode as a map, got %T", enqueued[0].Config["templateData"])
	r.Equal("http://127.0.0.1:4000/dash0/orgs/mr-url-org/organization/requests",
		templateData["RequestsURL"])
}
