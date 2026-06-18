package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// requestWithUserOrg builds a bunrouter request carrying the given user and org
// in context, mirroring what RequireAuth + RequireOrgAccess place there.
func requestWithUserOrg(user *models.User, org *models.Organization) bunrouter.Request {
	ctx := context.Background()
	ctx = context.WithValue(ctx, base.ContextKeyUser, user)
	ctx = context.WithValue(ctx, base.ContextKeyOrganization, org)
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/orgs/test/check-jobs", http.NoBody)

	return bunrouter.NewRequest(r)
}

func TestRequireOrgAdmin_AuthMatrix(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("admorg", "Admin Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	mkUser := func(email string, super bool) *models.User {
		u := models.NewUser(email)
		u.SuperAdmin = super
		r.NoError(dbSvc.CreateUser(ctx, u))

		return u
	}

	mkMember := func(u *models.User, role models.MemberRole) {
		m := models.NewOrganizationMember(org.UID, u.UID, role)
		r.NoError(dbSvc.CreateOrganizationMember(ctx, m))
	}

	adminUser := mkUser("admin@example.com", false)
	mkMember(adminUser, models.MemberRoleAdmin)

	regularUser := mkUser("user@example.com", false)
	mkMember(regularUser, models.MemberRoleUser)

	viewerUser := mkUser("viewer@example.com", false)
	mkMember(viewerUser, models.MemberRoleViewer)

	superUser := mkUser("super@example.com", true)
	// Super admin needs no membership.

	nonMember := mkUser("stranger@example.com", false)

	cfg := &config.Config{}
	authMw := middleware.NewAuthMiddleware(nil, dbSvc, cfg)

	guarded := authMw.RequireOrgAdmin(func(w http.ResponseWriter, _ bunrouter.Request) error {
		w.WriteHeader(http.StatusOK)

		return nil
	})

	tests := []struct {
		name       string
		user       *models.User
		wantStatus int
	}{
		{"org admin allowed", adminUser, http.StatusOK},
		{"super admin allowed", superUser, http.StatusOK},
		{"regular user forbidden", regularUser, http.StatusForbidden},
		{"viewer forbidden", viewerUser, http.StatusForbidden},
		{"non-member forbidden", nonMember, http.StatusForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rr := require.New(t)
			w := httptest.NewRecorder()
			_ = guarded(w, requestWithUserOrg(tc.user, org))
			rr.Equal(tc.wantStatus, w.Code)
		})
	}
}

func TestRequireSuperAdmin_AuthMatrix(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	cfg := &config.Config{}
	authMw := middleware.NewAuthMiddleware(nil, dbSvc, cfg)

	guarded := authMw.RequireSuperAdmin(func(w http.ResponseWriter, _ bunrouter.Request) error {
		w.WriteHeader(http.StatusOK)

		return nil
	})

	regular := models.NewUser("reg@example.com")
	super := models.NewUser("sup@example.com")
	super.SuperAdmin = true

	mkReq := func(u *models.User) bunrouter.Request {
		c := context.WithValue(context.Background(), base.ContextKeyUser, u)
		req := httptest.NewRequestWithContext(c, http.MethodGet, "/api/v1/system/jobs", http.NoBody)

		return bunrouter.NewRequest(req)
	}

	wRegular := httptest.NewRecorder()
	_ = guarded(wRegular, mkReq(regular))
	r.Equal(http.StatusForbidden, wRegular.Code)

	wSuper := httptest.NewRecorder()
	_ = guarded(wSuper, mkReq(super))
	r.Equal(http.StatusOK, wSuper.Code)
}
