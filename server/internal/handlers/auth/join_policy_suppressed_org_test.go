package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/defaults"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// suppressedOrgCase is one row of the rule-6 carve-out table.
type suppressedOrgCase struct {
	name string
	// mode is SP_DEPLOYMENT_MODE.
	mode string
	// demoSlug overrides demo.org_slug ("" keeps the default, "demo").
	demoSlug string
	// orgSlug is the org whose login page the callback came from.
	orgSlug string
	// wantRequest is whether a membership_requests row must exist after.
	wantRequest bool
}

func suppressedOrgCases() []suppressedOrgCase {
	saas, self := config.DeploymentModeSaaS, config.DeploymentModeSelfHosted

	return []suppressedOrgCase{
		{name: "saas + default org is suppressed", mode: saas, orgSlug: defaults.Organization},
		{
			name: "self-hosted + default org still opens a request",
			mode: self, orgSlug: defaults.Organization, wantRequest: true,
		},
		{name: "saas + demo org is suppressed", mode: saas, orgSlug: "demo"},
		{name: "self-hosted + demo org is suppressed", mode: self, orgSlug: "demo"},
		{name: "custom demo slug is suppressed", mode: self, demoSlug: "showcase", orgSlug: "showcase"},
		{
			name: "org literally named demo is ordinary under a custom demo slug",
			mode: self, demoSlug: "showcase", orgSlug: "demo", wantRequest: true,
		},
		{name: "saas + regular org opens a request", mode: saas, orgSlug: "acme", wantRequest: true},
		{name: "self-hosted + regular org opens a request", mode: self, orgSlug: "acme", wantRequest: true},
	}
}

// TestJoinOrgViaLoginSuppressedOrgs is the admission table for rule 6's
// carve-outs: the demo org (any mode) and the SaaS default org never file a
// membership request, whether the account is brand new or returning (the join
// policy no longer distinguishes the two). Regular orgs, and the self-hosted
// default org, are the negative controls and must still open a pending request.
func TestJoinOrgViaLoginSuppressedOrgs(t *testing.T) {
	t.Parallel()

	for _, tt := range suppressedOrgCases() {
		for _, returning := range []bool{false, true} {
			name := tt.name + " / new account"
			if returning {
				name = tt.name + " / returning account"
			}

			t.Run(name, func(t *testing.T) {
				t.Parallel()

				svc, dbSvc, ctx := setupAuthTestService(t)
				svc.fullCfg.Deployment.Mode = tt.mode
				svc.fullCfg.Demo.OrgSlug = tt.demoSlug

				// Seeded so the org is past the zero-member bootstrap rule and
				// really reaches rule 6.
				org := joinTestOrg(ctx, t, dbSvc, tt.orgSlug, true)
				user := joinTestUser(ctx, t, dbSvc, "newcomer@unknown.example")

				if returning {
					// A first sign-in already happened; the second one must
					// behave the same way.
					_, _, err := svc.JoinOrgViaLogin(ctx, org, user, WithLoginMethod(signupMethodGoogle))
					require.NoError(t, err)
				}

				member, pending, err := svc.JoinOrgViaLogin(ctx, org, user, WithLoginMethod(signupMethodGoogle))
				require.NoError(t, err)

				// The carve-out only removes the request, never grants access.
				require.Nil(t, member)
				require.True(t, pending)

				_, memberErr := dbSvc.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
				require.Error(t, memberErr, "a refused login must leave no organization_members row")

				request, reqErr := dbSvc.GetMembershipRequestByOrgAndUser(ctx, org.UID, user.UID)

				if tt.wantRequest {
					require.NoError(t, reqErr)
					require.NotNil(t, request)
					require.Equal(t, models.MembershipRequestStatusPending, request.Status)

					return
				}

				require.True(t, reqErr != nil || request == nil,
					"%s must not queue a join request", tt.orgSlug)
			})
		}
	}
}

// TestCompleteOrgLoginPendingOrgSlug pins the *naming* half: a suppressed
// pending session names no org and its handoff redirect carries no
// membershipPending param, while an ordinary pending one still names the org.
func TestCompleteOrgLoginPendingOrgSlug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mode     string
		orgSlug  string
		wantSlug string
	}{
		{"saas default org (returning user) names no org", config.DeploymentModeSaaS, defaults.Organization, ""},
		{"demo org names no org", config.DeploymentModeSelfHosted, "demo", ""},
		{"demo org on saas names no org", config.DeploymentModeSaaS, "demo", ""},
		{"regular org still names the org", config.DeploymentModeSaaS, "acme", "acme"},
		{
			"self-hosted default org still names the org",
			config.DeploymentModeSelfHosted, defaults.Organization, defaults.Organization,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, dbSvc, ctx := setupAuthTestService(t)
			svc.fullCfg.Deployment.Mode = tt.mode

			org := joinTestOrg(ctx, t, dbSvc, tt.orgSlug, true)
			user := joinTestUser(ctx, t, dbSvc, "fresh@unknown.example")

			result, err := svc.CompleteOrgLogin(ctx, org, user, WithLoginMethod(signupMethodGoogle))
			require.NoError(t, err)
			require.True(t, result.Pending)
			require.Equal(t, tt.wantSlug, result.PendingOrgSlug)

			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/auth/google/callback", nil)

			require.NoError(t, finishProviderCallback(rec, req, dbSvc, "google", "/d/orgs/"+tt.orgSlug, &ProviderOutcome{
				AccessToken:     result.AccessToken,
				RefreshToken:    result.RefreshToken,
				ExpiresIn:       result.ExpiresIn,
				OrgSlug:         org.Slug,
				UserUID:         user.UID,
				Pending:         result.Pending,
				FallbackOrgSlug: result.FallbackOrgSlug,
				PendingOrgSlug:  result.PendingOrgSlug,
			}))

			location, err := url.Parse(rec.Header().Get("Location"))
			require.NoError(t, err)
			require.Equal(t, tt.wantSlug, location.Query().Get(pendingMembershipParam))
			require.Equal(t, tt.wantSlug != "", location.Query().Has(pendingMembershipParam))
		})
	}
}

// TestCreateMembershipRequestRefusedForSuppressedOrgs: the explicit request
// endpoint refuses the orgs whose sign-ins never open a request, and accepts
// the self-hosted default org and regular orgs.
func TestCreateMembershipRequestRefusedForSuppressedOrgs(t *testing.T) {
	t.Parallel()

	saas, self := config.DeploymentModeSaaS, config.DeploymentModeSelfHosted

	tests := []struct {
		name    string
		mode    string
		orgSlug string
		refused bool
	}{
		{"demo on self-hosted", self, "demo", true},
		{"demo on saas", saas, "demo", true},
		{"default on saas", saas, defaults.Organization, true},
		{"default on self-hosted", self, defaults.Organization, false},
		{"regular org on saas", saas, "acme", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, org, _, candidate := seedMembershipFixtures(t, tt.orgSlug)
			svc.fullCfg.Deployment.Mode = tt.mode

			summary, err := svc.CreateMembershipRequest(t.Context(), candidate.UID,
				MembershipRequestCreateRequest{OrgSlug: org.Slug})

			if tt.refused {
				require.ErrorIs(t, err, ErrOrgNotAcceptingRequests)
				require.Nil(t, summary)

				return
			}

			require.NoError(t, err)
			require.Equal(t, models.MembershipRequestStatusPending, summary.Status)
		})
	}
}

// TestCreateMembershipRequestHandlerRefusedForSuppressedOrgs drives the real
// HTTP handler: the demo org and the SaaS default org answer 403
// ORG_NOT_ACCEPTING_REQUESTS, a regular org answers 201.
func TestCreateMembershipRequestHandlerRefusedForSuppressedOrgs(t *testing.T) {
	t.Parallel()

	saas, self := config.DeploymentModeSaaS, config.DeploymentModeSelfHosted

	tests := []struct {
		name       string
		mode       string
		orgSlug    string
		wantStatus int
	}{
		{"demo", self, "demo", http.StatusForbidden},
		{"saas default", saas, defaults.Organization, http.StatusForbidden},
		{"regular org", saas, "acme", http.StatusCreated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, org, _, candidate := seedMembershipFixtures(t, tt.orgSlug)
			svc.fullCfg.Deployment.Mode = tt.mode
			handler := NewHandler(svc, svc.fullCfg)

			reqCtx := context.WithValue(t.Context(), base.ContextKeyClaims, &Claims{UserUID: candidate.UID})
			httpReq := httptest.NewRequestWithContext(reqCtx, http.MethodPost,
				"/api/v1/auth/membership-requests",
				strings.NewReader(`{"orgSlug":"`+org.Slug+`"}`))
			rec := httptest.NewRecorder()

			require.NoError(t, handler.CreateMembershipRequestHandler(rec, httpReq))
			require.Equal(t, tt.wantStatus, rec.Code)

			if tt.wantStatus == http.StatusForbidden {
				require.Equal(t, "ORG_NOT_ACCEPTING_REQUESTS", decodeErrorCode(t, rec))
			}
		})
	}
}
