package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// TestRequireOrgOwnerAndAdmin_AuthMatrix pins BOTH gates against the same
// membership set, because the owner role is only correct if two opposite things
// hold at once: an owner clears the admin gate (owner outranks admin), and an
// admin does NOT clear the owner gate. Testing either in isolation would let a
// one-sided regression through.
func TestRequireOrgOwnerAndAdmin_AuthMatrix(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("ownorg", "Owner Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	mkUser := func(email string, super bool) *models.User {
		user := models.NewUser(email)
		user.SuperAdmin = super
		r.NoError(dbSvc.CreateUser(ctx, user))

		return user
	}

	mkMember := func(user *models.User, role models.MemberRole) {
		r.NoError(dbSvc.CreateOrganizationMember(ctx, models.NewOrganizationMember(org.UID, user.UID, role)))
	}

	ownerUser := mkUser("owner@example.com", false)
	mkMember(ownerUser, models.MemberRoleOwner)

	adminUser := mkUser("admin@example.com", false)
	mkMember(adminUser, models.MemberRoleAdmin)

	regularUser := mkUser("user@example.com", false)
	mkMember(regularUser, models.MemberRoleUser)

	viewerUser := mkUser("viewer@example.com", false)
	mkMember(viewerUser, models.MemberRoleViewer)

	superUser := mkUser("super@example.com", true)
	nonMember := mkUser("stranger@example.com", false)

	authMw := middleware.NewAuthMiddleware(nil, dbSvc, &config.Config{})

	okHandler := func(w http.ResponseWriter, _ *http.Request) error {
		w.WriteHeader(http.StatusOK)

		return nil
	}

	ownerGuarded := authMw.RequireOrgOwner(okHandler)
	adminGuarded := authMw.RequireOrgAdmin(okHandler)

	tests := []struct {
		name            string
		user            *models.User
		wantOwnerStatus int
		wantAdminStatus int
	}{
		{"owner clears both gates", ownerUser, http.StatusOK, http.StatusOK},
		{"admin clears only the admin gate", adminUser, http.StatusForbidden, http.StatusOK},
		{"user clears neither", regularUser, http.StatusForbidden, http.StatusForbidden},
		{"viewer clears neither", viewerUser, http.StatusForbidden, http.StatusForbidden},
		{"non-member clears neither", nonMember, http.StatusForbidden, http.StatusForbidden},
		{"super admin clears both", superUser, http.StatusOK, http.StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rr := require.New(t)

			ownerRec := httptest.NewRecorder()
			_ = ownerGuarded(ownerRec, requestWithUserOrg(tc.user, org))
			rr.Equal(tc.wantOwnerStatus, ownerRec.Code)

			adminRec := httptest.NewRecorder()
			_ = adminGuarded(adminRec, requestWithUserOrg(tc.user, org))
			rr.Equal(tc.wantAdminStatus, adminRec.Code)

			// A refusal must use the repo's standard error shape.
			if tc.wantOwnerStatus == http.StatusForbidden {
				var body map[string]any
				rr.NoError(json.Unmarshal(ownerRec.Body.Bytes(), &body))
				rr.Equal("FORBIDDEN", body["code"])
				rr.Equal("Owner access required", body["title"])
			}
		})
	}
}
