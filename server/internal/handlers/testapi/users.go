package testapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// CreateUserRequest represents the request body for creating a zero-org test user.
type CreateUserRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
	// MustChangePassword seeds the forced-rotation flag (spec 2026-08-23-04).
	// The state it produces is otherwise only reachable by booting against a
	// brand-new database, which an e2e run cannot do without destroying the
	// shared test fixture — so the e2e spec for the rotation screen asks for it
	// here instead. Test-only route; the flag itself is ordinary production
	// behavior.
	MustChangePassword bool `json:"mustChangePassword"`
	// PersonalOrg lets the first sign-in give the user a personal org, as for
	// any real new user (spec 2026-10-30-04). Off by default: this route
	// exists to seed a zero-org identity, so the user is marked as having had
	// (and deleted) their one personal org, which keeps the sign-in on /no-org.
	PersonalOrg bool `json:"personalOrg"`
}

// CreateUser creates a pre-confirmed, org-less user directly in the
// database, bypassing the public self-registration flow entirely. That flow
// (POST /auth/register + /auth/confirm-registration) requires
// `auth.registration_email_pattern` to be configured, which is not
// guaranteed in every environment this test suite runs against; e2e specs
// that need a fresh zero-org identity (e.g. create-org.spec.ts) use this
// endpoint instead so they exercise the real /auth/login "no org" path
// (LoginActionNoOrg in the auth service) without depending on registration
// being enabled.
//
// Test-only: only routed when SP_RUNMODE=test, same as the other
// /api/v1/test/* routes registered in server.go.
// POST /api/v1/test/users.
func (h *Handler) CreateUser(writer http.ResponseWriter, req *http.Request) error {
	var body CreateUserRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.writeError(writer, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid request body")
	}

	if body.Email == "" {
		return h.writeError(writer, http.StatusBadRequest, "VALIDATION_ERROR", "Field 'email' is required")
	}

	if body.Password == "" {
		return h.writeError(writer, http.StatusBadRequest, "VALIDATION_ERROR", "Field 'password' is required")
	}

	existing, err := h.dbService.GetUserByEmail(req.Context(), body.Email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return h.writeInternalError(writer, err)
	}

	if existing != nil {
		return h.writeError(writer, http.StatusConflict, "CONFLICT", "Email already taken")
	}

	hash, err := passwords.Hash(body.Password)
	if err != nil {
		return h.writeInternalError(writer, err)
	}

	user := models.NewUser(body.Email)
	user.Name = body.Name
	user.PasswordHash = &hash
	user.MustChangePassword = body.MustChangePassword

	now := time.Now()
	user.EmailVerifiedAt = &now

	if err := h.dbService.CreateUser(req.Context(), user); err != nil {
		return h.writeInternalError(writer, err)
	}

	if !body.PersonalOrg {
		if err := h.retirePersonalOrg(req.Context(), user); err != nil {
			return h.writeInternalError(writer, err)
		}
	}

	return h.writeJSON(writer, http.StatusCreated, map[string]any{
		"uid":                user.UID,
		"email":              user.Email,
		"mustChangePassword": user.MustChangePassword,
	})
}

// retirePersonalOrg records a soft-deleted personal org for the user: a user
// gets one personal org in their lifetime, so none is created at sign-in.
func (h *Handler) retirePersonalOrg(ctx context.Context, user *models.User) error {
	org := models.NewOrganization("retired-"+user.UID[:8], "Retired personal org")
	org.AutoPersonalOwnerUID = &user.UID

	if err := h.dbService.CreateOrganization(ctx, org); err != nil {
		return err
	}

	return h.dbService.DeleteOrganization(ctx, org.UID)
}
