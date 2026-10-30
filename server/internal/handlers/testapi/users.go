package testapi

import (
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
	// PersonalOrg lets the user's first sign-in create their personal org, as
	// in production (spec 2026-10-30-04). By default the seeded user stays
	// org-less: a pending membership request to a decoy org is filed for them,
	// which is what keeps the sign-in from creating a personal org, so the
	// many specs that need a zero-org identity keep working unchanged.
	PersonalOrg bool `json:"personalOrg"`
}

// decoyOrgSlug is the org the default zero-org test user "asked to join".
const decoyOrgSlug = "e2e-decoy"

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
		if err := h.fileDecoyRequest(req, user.UID); err != nil {
			return h.writeInternalError(writer, err)
		}
	}

	return h.writeJSON(writer, http.StatusCreated, map[string]any{
		"uid":                user.UID,
		"email":              user.Email,
		"mustChangePassword": user.MustChangePassword,
	})
}

// fileDecoyRequest files a pending membership request for the user to the
// decoy org, creating that org on first use.
func (h *Handler) fileDecoyRequest(req *http.Request, userUID string) error {
	ctx := req.Context()

	org, err := h.dbService.GetOrganizationBySlug(ctx, decoyOrgSlug)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	if org == nil {
		org = models.NewOrganization(decoyOrgSlug, "E2E decoy")
		if err := h.dbService.CreateOrganization(ctx, org); err != nil {
			return err
		}
	}

	return h.dbService.CreateMembershipRequest(ctx, models.NewMembershipRequest(org.UID, userUID, nil))
}
