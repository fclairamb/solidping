// Package unsubscribe implements the public (unauthenticated) per-recipient
// email unsubscribe surface: RFC 8058 one-click POST, a minimal GET
// confirmation page, and the re-subscribe undo. Lives outside handlers/incidents
// because it has no incident in scope — the unsubscribe token identifies an
// org + email + optional check, nothing more.
package unsubscribe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/incidentlinks"
)

// Sentinel errors surfaced to the handler, which maps each to a distinct
// page/response (mirrors the ack magic-link handler's approach).
var (
	// ErrMissingToken means the request has no token query parameter.
	ErrMissingToken = errors.New("unsubscribe: missing token")
	// ErrTokenInvalid wraps any incidentlinks verification failure (expired,
	// malformed, wrong signature, or wrong purpose) into one bucket — the
	// caller must not distinguish these to an unauthenticated requester
	// (doing so would leak which failure mode almost succeeded).
	ErrTokenInvalid = errors.New("unsubscribe: invalid or expired token")
	// ErrOrgNotFound means the token's org slug no longer resolves to an
	// organization (e.g. deleted after the email was sent).
	ErrOrgNotFound = errors.New("unsubscribe: organization not found")
)

// Scope selects what a POST /unsubscribe request suppresses.
type Scope string

// Scope values. ScopeFromToken uses whatever the token encodes (the RFC 8058
// one-click path and the GET confirmation page's default action); ScopeCheck
// and ScopeOrg let the GET confirmation page's two explicit buttons override
// the token's default when the token was check-scoped but the recipient
// wants the broader "all alerts" scope (or vice versa isn't offered — you
// can't narrow an org-wide token to one check, since the token never
// recorded a check to narrow to).
const (
	ScopeFromToken Scope = ""
	ScopeCheck     Scope = "check"
	ScopeOrg       Scope = "org"
)

// Result is what a successful unsubscribe/resubscribe reports back to the
// handler for rendering.
type Result struct {
	OrgSlug   string
	Email     string
	CheckUID  string // "" for org-wide
	CheckName string // "" when CheckUID is "" or the check no longer exists
	// SuppressionUID is the created (or pre-existing) row's UID — the
	// confirmation page's undo link embeds this so ResubscribeByUID can
	// target the exact row without re-deriving it from the token.
	SuppressionUID string
}

// Service implements the unsubscribe business logic against db.Service and
// the shared incidentlinks token verifier.
type Service struct {
	dbService db.Service
	secret    []byte
}

// NewService creates a new unsubscribe service. secret is the HMAC key
// (cfg.Auth.JWTSecret) — the same secret used for ack tokens; purpose
// tagging (not key separation) is what keeps the two token kinds apart.
func NewService(dbService db.Service, secret []byte) *Service {
	return &Service{dbService: dbService, secret: secret}
}

// resolve verifies token and resolves it to an org UID (from the org slug)
// plus the effective check scope, applying the requested scope override
// (ScopeCheck/ScopeOrg) when given. ScopeCheck on an org-wide token is a
// no-op (there's no check to narrow to — the token never carried one), so it
// falls back to the token's own scope.
func (s *Service) resolve(
	ctx context.Context, token string, scope Scope,
) (*models.Organization, string, string, error) {
	payload, verr := incidentlinks.VerifyUnsubscribe(s.secret, token)
	if verr != nil {
		return nil, "", "", ErrTokenInvalid
	}

	org, err := s.dbService.GetOrganizationBySlug(ctx, payload.OrgSlug)
	if err != nil {
		return nil, "", "", ErrOrgNotFound
	}

	effectiveCheckUID := payload.CheckUID

	switch scope {
	case ScopeOrg:
		effectiveCheckUID = "" // widen to org-wide regardless of what the token carried
	case ScopeCheck:
		// Narrowing to "this check only" only makes sense when the token
		// itself carried a check — an org-wide token has no check to narrow
		// to, so this is a no-op and the org-wide scope is kept.
	case ScopeFromToken:
		// Use the token's own scope unchanged.
	}

	return org, payload.Email, effectiveCheckUID, nil
}

// Unsubscribe verifies token and idempotently creates a suppression row for
// the resolved (org, email, checkUID) scope. Calling it twice for the same
// token/scope must not error — this is what makes the RFC 8058 one-click
// POST safe to retry, and what makes double-submitting the GET
// confirmation-page form harmless.
//
// source is one of models.EmailSuppressionSource* — "link" for the
// confirmation-page form, "header" for the one-click POST (mail clients
// auto-submit this without the recipient seeing a page).
func (s *Service) Unsubscribe(ctx context.Context, token string, scope Scope, source string) (*Result, error) {
	org, email, checkUID, err := s.resolve(ctx, token, scope)
	if err != nil {
		return nil, err
	}

	var checkUIDPtr *string
	if checkUID != "" {
		checkUIDPtr = &checkUID
	}

	sup := models.NewEmailSuppression(org.UID, email, checkUIDPtr, source)

	if createErr := s.dbService.CreateEmailSuppression(ctx, sup); createErr != nil {
		// The unique index (one row per (org, email, check_uid) scope)
		// rejects a duplicate — that is the expected, harmless outcome of a
		// retried one-click POST or a double-submitted form, not a real
		// error. Re-check whether the exact suppression now exists (either
		// this call's own row, raced by a concurrent duplicate, or a
		// genuinely pre-existing one) before deciding whether to surface
		// createErr to the caller.
		existing, findErr := s.findExisting(ctx, org.UID, email, checkUID)
		if findErr != nil || existing == nil {
			return nil, createErr //nolint:wrapcheck // createErr is already descriptive; findErr would obscure it
		}

		s.dropFromReportSchedules(ctx, org.UID, email, checkUID)

		return s.toResult(ctx, org.Slug, existing), nil
	}

	s.dropFromReportSchedules(ctx, org.UID, email, checkUID)

	return s.toResult(ctx, org.Slug, sup), nil
}

// dropFromReportSchedules removes an org-wide unsubscriber from every uptime
// report schedule in the org (spec 2026-08-20-01).
//
// The suppression list alone would already stop the mail, but leaving the
// address on the schedule means the operator sees it in the recipient list and
// can "fix" it by re-saving — silently mailing someone who asked to stop. The
// unsubscribe is only really honored when the address is gone from the source
// of truth too.
//
// Scoped to org-wide unsubscribes on purpose: a check-scoped unsubscribe says
// "stop paging me about THIS check", which is not a statement about a monthly
// digest covering forty others.
//
// Best-effort: the suppression row is already committed and is what actually
// blocks delivery, so a failure here must not turn a successful unsubscribe
// into an error page.
func (s *Service) dropFromReportSchedules(ctx context.Context, orgUID, email, checkUID string) {
	if checkUID != "" {
		return
	}

	if _, err := s.dbService.RemoveRecipientFromReportSchedules(ctx, orgUID, email); err != nil {
		slog.WarnContext(ctx, "Failed to remove unsubscriber from report schedules",
			"org_uid", orgUID, "error", err)
	}
}

// findExisting looks for a suppression matching the exact (org, email,
// checkUID) scope — checkUID == "" means "the org-wide row", not "any row
// for this email". Unlike IsEmailSuppressed (which OR's the two scopes to
// answer "is delivery blocked"), this must match the CALLER's specific scope
// so the idempotency check in Unsubscribe doesn't paper over a genuinely
// failed insert by finding an unrelated row.
func (s *Service) findExisting(ctx context.Context, orgUID, email, checkUID string) (*models.EmailSuppression, error) {
	rows, err := s.dbService.ListEmailSuppressions(ctx, orgUID)
	if err != nil {
		return nil, fmt.Errorf("list email suppressions while checking idempotency: %w", err)
	}

	for _, row := range rows {
		if row.Email != email {
			continue
		}

		rowCheckUID := ""
		if row.CheckUID != nil {
			rowCheckUID = *row.CheckUID
		}

		if rowCheckUID == checkUID {
			return row, nil
		}
	}

	return nil, nil //nolint:nilnil // "not found" is a valid, non-error outcome here
}

// ResubscribeByUID deletes a suppression row while its issuing token is
// still valid — the GET confirmation page's "undo" link and the "one-click
// unsubscribe went to the wrong scope" recovery path. Re-verifies the token
// (not just trusting the UID in the URL) so an expired/tampered undo link
// can't delete an arbitrary row by UID guessing; the token's org+email must
// also match the row being deleted.
func (s *Service) ResubscribeByUID(ctx context.Context, token, suppressionUID string) error {
	org, email, _, err := s.resolve(ctx, token, ScopeFromToken)
	if err != nil {
		return err
	}

	existing, err := s.dbService.GetEmailSuppression(ctx, org.UID, suppressionUID)
	if err != nil {
		return ErrTokenInvalid // treat "not this org's row" the same as "invalid token" — no information leak
	}

	if existing.Email != email {
		return ErrTokenInvalid
	}

	if delErr := s.dbService.DeleteEmailSuppression(ctx, suppressionUID); delErr != nil {
		return fmt.Errorf("resubscribe: %w", delErr)
	}

	return nil
}

// toResult builds the Result the handler renders, resolving the check name
// for display when the scope is check-specific. A check lookup failure
// (deleted check) is not fatal — CheckName just stays empty and the page
// falls back to showing the raw scope.
func (s *Service) toResult(ctx context.Context, orgSlug string, sup *models.EmailSuppression) *Result {
	res := &Result{
		OrgSlug:        orgSlug,
		Email:          sup.Email,
		SuppressionUID: sup.UID,
	}

	if sup.CheckUID == nil {
		return res
	}

	res.CheckUID = *sup.CheckUID

	check, err := s.dbService.GetCheck(ctx, sup.OrganizationUID, *sup.CheckUID)
	if err != nil || check == nil {
		return res
	}

	if check.Name != nil {
		res.CheckName = *check.Name
	} else if check.Slug != nil {
		res.CheckName = *check.Slug
	}

	return res
}
