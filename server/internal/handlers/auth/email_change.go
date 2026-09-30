package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// maxEmailLength is the practical upper bound of an address (RFC 5321 path
// limit).
const maxEmailLength = 254

// Email-change refusals (spec 2026-09-30-08).
var (
	// ErrInvalidEmail: the new address is empty or not a plain address.
	// Mapped to 400 VALIDATION_ERROR.
	ErrInvalidEmail = errors.New("invalid email address")
	// ErrEmailChangeNoPassword: the account has no local password (it signs in
	// through OAuth, OIDC, SAML or LDAP), so there is nothing to re-verify the
	// caller with. Mapped to 403.
	ErrEmailChangeNoPassword = errors.New(
		"this account has no password: change your email address at your identity provider")
	// ErrEmailChangeDemo: the shared demo principal's address is fixed.
	ErrEmailChangeDemo = errors.New("the shared demo account's email address cannot be changed")
	// ErrEmailChangeNotSuperAdmin: the system endpoint's actor is not a super
	// admin. The route is already behind RequireSuperAdmin; this is the
	// service's own check.
	ErrEmailChangeNotSuperAdmin = errors.New("only a super admin can change another user's email")
)

// Who changed an email, recorded on auth.email_changed.
const (
	emailChangedBySelf       = "self"
	emailChangedBySuperAdmin = "super_admin"

	auditKeyOldEmail  = "old_email"
	auditKeyNewEmail  = "new_email"
	auditKeyChangedBy = "changed_by"

	// tmplKeyChangedAt is the timestamp field of the security-notice email
	// templates (password-changed.html, email-changed.html).
	tmplKeyChangedAt = "ChangedAt"
)

// normalizeEmail trims and lowercases an address and checks it is a single
// plain address ("alice@acme.com", never "Alice <alice@acme.com>").
func normalizeEmail(raw string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if normalized == "" || len(normalized) > maxEmailLength {
		return "", ErrInvalidEmail
	}

	addr, err := mail.ParseAddress(normalized)
	if err != nil || addr.Name != "" || addr.Address != normalized {
		return "", ErrInvalidEmail
	}

	at := strings.LastIndex(normalized, "@")
	if at <= 0 || at == len(normalized)-1 {
		return "", ErrInvalidEmail
	}

	return normalized, nil
}

// emailChange describes one validated email change about to be applied.
type emailChange struct {
	user      *models.User
	newEmail  string
	changedBy string
	actorUID  string
	// keepRefreshUID is the caller's own session, spared by the revocation
	// sweep ("" revokes every session).
	keepRefreshUID string
	// name is an optional profile name change applied in the same UPDATE.
	name *string
}

// ensureEmailAvailable returns ErrEmailAlreadyTaken when another non-deleted
// user already holds the address (case-insensitively).
func (s *Service) ensureEmailAvailable(ctx context.Context, userUID, newEmail string) error {
	existing, err := s.db.GetUserByEmail(ctx, newEmail)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	if existing != nil && existing.UID != userUID {
		return ErrEmailAlreadyTaken
	}

	return nil
}

// applyEmailChange persists the new address, clears email_verified_at,
// revokes the user's other sessions, notifies the old address and records
// auth.email_changed. The caller has already authorized the change.
func (s *Service) applyEmailChange(ctx context.Context, change *emailChange) error {
	if err := s.ensureEmailAvailable(ctx, change.user.UID, change.newEmail); err != nil {
		return err
	}

	oldEmail := change.user.Email

	if err := s.db.UpdateUser(ctx, change.user.UID, &models.UserUpdate{
		Name:                 change.name,
		Email:                &change.newEmail,
		ClearEmailVerifiedAt: true,
	}); err != nil {
		// The pre-check above loses a race against a concurrent signup or
		// change: the unique index is the real guard.
		if db.IsUniqueViolation(err) {
			return ErrEmailAlreadyTaken
		}

		return fmt.Errorf("failed to update email: %w", err)
	}

	// Same policy as a password change: session refresh tokens die (except
	// the caller's own), PATs survive as separately managed credentials.
	s.revokeRefreshTokensForUserExcept(ctx, change.user.UID, change.keepRefreshUID)

	changedAt := time.Now().UTC()

	if s.fullCfg != nil && s.fullCfg.Email.Enabled {
		s.enqueueEmail(ctx, "", oldEmail, email.TemplateEmailChanged, map[string]any{
			tmplKeyChangedAt: changedAt.Format(time.RFC1123),
			"OldEmail":       oldEmail,
			"NewEmail":       change.newEmail,
		})
	} else {
		slog.WarnContext(ctx, "Email sending is disabled: the previous address was not told about the email change",
			"userUid", change.user.UID)
	}

	s.recordEmailChanged(ctx, change, oldEmail)

	return nil
}

// recordEmailChanged writes auth.email_changed in every organization the user
// belongs to, so each org's admins see that a member's sign-in changed.
func (s *Service) recordEmailChanged(ctx context.Context, change *emailChange, oldEmail string) {
	members, err := s.db.ListMembersByUser(ctx, change.user.UID)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to list memberships for the email-change audit", "error", err)

		return
	}

	actorCtx := auditActorCtx(ctx, change.actorUID, Context{})

	for _, member := range members {
		audit.Record(actorCtx, s.db, member.OrganizationUID, models.EventTypeAuthEmailChanged,
			audit.Target{Type: auditTargetUser, UID: change.user.UID, Name: change.newEmail},
			models.JSONMap{
				auditKeyOldEmail:  oldEmail,
				auditKeyNewEmail:  change.newEmail,
				auditKeyChangedBy: change.changedBy,
			})
	}
}

// verifyEmailChangePassword re-authenticates a self-service email change. The
// current-password check is a brute-force surface, like the one in
// ChangePassword, so it shares that per-user counter.
func (s *Service) verifyEmailChangePassword(ctx context.Context, user *models.User, currentPassword *string) error {
	limited, err := s.bumpChangePasswordCounter(ctx, user.UID)
	if err != nil {
		return fmt.Errorf("failed to bump change-password counter: %w", err)
	}

	if limited {
		return ErrRateLimited
	}

	given := ""
	if currentPassword != nil {
		given = *currentPassword
	}

	// Always run the verify, even for an empty password, so "missing" and
	// "wrong" cost the same time.
	if !passwords.Verify(given, *user.PasswordHash) {
		return ErrInvalidCurrentPassword
	}

	return nil
}

// changeOwnEmail validates and applies a self-service email change requested
// through PATCH /auth/me. It returns (false, nil) when the address is
// unchanged, so the caller only applies the name.
func (s *Service) changeOwnEmail(ctx context.Context, claims *Claims, req UpdateProfileRequest) (bool, error) {
	newEmail, err := normalizeEmail(*req.Email)
	if err != nil {
		return false, err
	}

	if claims.IsImpersonation() {
		return false, ErrImpersonationForbidden
	}

	user, err := s.db.GetUser(ctx, claims.UserUID)
	if err != nil || user == nil {
		return false, ErrUserNotFound
	}

	if strings.EqualFold(user.Email, newEmail) {
		return false, nil
	}

	if user.Demo || claims.Demo {
		return false, ErrEmailChangeDemo
	}

	if user.PasswordHash == nil || *user.PasswordHash == "" {
		return false, ErrEmailChangeNoPassword
	}

	if err := s.verifyEmailChangePassword(ctx, user, req.CurrentPassword); err != nil {
		return false, err
	}

	if err := s.applyEmailChange(ctx, &emailChange{
		user:           user,
		newEmail:       newEmail,
		changedBy:      emailChangedBySelf,
		actorUID:       user.UID,
		keepRefreshUID: claims.RefreshUID,
		name:           req.Name,
	}); err != nil {
		return false, err
	}

	if _, err := s.db.DeleteStateEntry(ctx, nil, changePasswordCountKeyPrefix+user.UID); err != nil {
		slog.DebugContext(ctx, "Failed to delete change-password counter", "error", err)
	}

	return true, nil
}

// AdminUpdateUserRequest is the body of PATCH /api/v1/system/users/:uid.
// Every field is optional; only email exists today.
type AdminUpdateUserRequest struct {
	Email *string `json:"email,omitempty"`
}

// AdminUpdateUserResponse is the updated user, as the system directory shows
// it.
type AdminUpdateUserResponse struct {
	UID           string `json:"uid"`
	Email         string `json:"email"`
	Name          string `json:"name"`
	EmailVerified bool   `json:"emailVerified"`
}

// AdminUpdateUser lets a super admin change any user's email (spec
// 2026-09-30-08). No password is asked of the target: the caller's super
// admin authentication is what authorizes it.
func (s *Service) AdminUpdateUser(
	ctx context.Context, claims *Claims, targetUID string, req AdminUpdateUserRequest,
) (*AdminUpdateUserResponse, error) {
	if claims.IsImpersonation() {
		return nil, ErrImpersonationForbidden
	}

	actor, err := s.db.GetUser(ctx, claims.UserUID)
	if err != nil || actor == nil || !actor.SuperAdmin {
		return nil, ErrEmailChangeNotSuperAdmin
	}

	target, err := s.db.GetUser(ctx, targetUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}

		return nil, err
	}

	if target == nil || target.DeletedAt != nil {
		return nil, ErrUserNotFound
	}

	if req.Email != nil {
		if changeErr := s.adminChangeEmail(ctx, claims, actor, target, *req.Email); changeErr != nil {
			return nil, changeErr
		}
	}

	updated, err := s.db.GetUser(ctx, target.UID)
	if err != nil {
		return nil, err
	}

	return &AdminUpdateUserResponse{
		UID:           updated.UID,
		Email:         updated.Email,
		Name:          updated.Name,
		EmailVerified: updated.EmailVerifiedAt != nil,
	}, nil
}

// adminChangeEmail applies a super admin's change of target's email. An
// unchanged address (in any case) is a no-op.
func (s *Service) adminChangeEmail(
	ctx context.Context, claims *Claims, actor, target *models.User, rawEmail string,
) error {
	newEmail, err := normalizeEmail(rawEmail)
	if err != nil {
		return err
	}

	if strings.EqualFold(target.Email, newEmail) {
		return nil
	}

	keep := ""
	if target.UID == actor.UID {
		// A super admin renaming themselves keeps their own session.
		keep = claims.RefreshUID
	}

	return s.applyEmailChange(ctx, &emailChange{
		user:           target,
		newEmail:       newEmail,
		changedBy:      emailChangedBySuperAdmin,
		actorUID:       actor.UID,
		keepRefreshUID: keep,
	})
}
