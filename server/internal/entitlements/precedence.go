package entitlements

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// AuditSourceBillingSuppressed is the `source` recorded on an audit row for a
// billing push that was accepted (HTTP 200) but deliberately NOT applied
// because an admin override holds the org's entitlements. It is a distinct
// label rather than a plain billing-service row so the audit trail can be read
// as "this is what billing wanted, and this is why it did not happen".
const AuditSourceBillingSuppressed = "billing-service:suppressed"

// AuditSourceAdminReleased is the `source` recorded when a superadmin releases
// an org back to billing: the stored row is dropped, so the next billing push
// (or, failing that, the deployment-mode defaults) applies again.
const AuditSourceAdminReleased = "admin:released"

// suppressedAttemptKey is where the rejected payload is parked inside the
// suppression audit's after-snapshot. The snapshot is otherwise the UNCHANGED
// stored row — which is the truthful "state after the write" — and this key is
// what keeps the discarded intent visible.
const suppressedAttemptKey = "suppressedAttempt"

// WriteOutcome reports what a write to an org's entitlements actually did.
//
// It exists because "accepted" and "applied" are not the same thing on the
// billing path: a push onto an admin override must answer 200 (billing must
// not error-loop over a decision we made on purpose) while changing nothing.
// A caller that only checked the error would report a lie to its operator.
type WriteOutcome struct {
	// Applied is false when the precedence rule discarded the write.
	Applied bool
	// SuppressedBy names the source that won. Only set when Applied is false.
	SuppressedBy models.EntitlementSource
}

// Apply is the single write path for entitlements, subject to the precedence
// rule: **an admin-sourced row wins until explicitly released**.
//
// A billing push onto an admin row is recorded in org_entitlement_audits as
// AuditSourceBillingSuppressed and otherwise ignored; every other write goes
// straight through to Set. Both HTTP front doors (the signed billing PUT/PATCH
// and the superadmin editor) call this, so neither can bypass the rule.
//
//nolint:gocritic // input is the wire shape, passed by value to match Set's contract
func (s *Service) Apply(
	ctx context.Context, orgUID string, input Entitlements, actor, reason string,
) (WriteOutcome, error) {
	if input.Source != models.EntitlementSourceBilling {
		if err := s.Set(ctx, orgUID, input, actor, reason); err != nil {
			return WriteOutcome{}, err
		}

		return WriteOutcome{Applied: true}, nil
	}

	previous, err := s.db.GetOrgEntitlements(ctx, orgUID)
	if err != nil {
		return WriteOutcome{}, fmt.Errorf("load previous entitlements: %w", err)
	}

	if previous == nil || previous.Payload.Source != models.EntitlementSourceAdmin {
		if err := s.Set(ctx, orgUID, input, actor, reason); err != nil {
			return WriteOutcome{}, err
		}

		return WriteOutcome{Applied: true}, nil
	}

	if err := s.recordSuppressedPush(ctx, orgUID, previous, input, actor, reason); err != nil {
		return WriteOutcome{}, err
	}

	slog.InfoContext(ctx, "billing entitlements push suppressed by an admin override",
		"orgUID", orgUID, "actor", actor)

	return WriteOutcome{Applied: false, SuppressedBy: models.EntitlementSourceAdmin}, nil
}

// recordSuppressedPush writes the audit-only row for a discarded billing push.
// before and after are both the unchanged stored row — nothing moved — with the
// rejected payload attached to after under suppressedAttemptKey.
//
//nolint:gocritic // attempted is the wire shape, passed by value like everywhere else
func (s *Service) recordSuppressedPush(
	ctx context.Context,
	orgUID string,
	previous *models.OrgEntitlements,
	attempted Entitlements,
	actor, reason string,
) error {
	snapshot, err := modelToJSON(previous)
	if err != nil {
		return fmt.Errorf("snapshot current: %w", err)
	}

	after, err := modelToJSON(previous)
	if err != nil {
		return fmt.Errorf("snapshot current: %w", err)
	}

	attemptedJSON, err := entitlementsToJSON(attempted)
	if err != nil {
		return fmt.Errorf("snapshot attempted: %w", err)
	}

	after[suppressedAttemptKey] = map[string]any(attemptedJSON)

	audit := models.NewOrgEntitlementAudit(
		orgUID, AuditSourceBillingSuppressed, actor, snapshot, after,
		suppressionReason(reason),
	)

	if err := s.db.CreateOrgEntitlementAudit(ctx, audit); err != nil {
		return fmt.Errorf("record suppressed push: %w", err)
	}

	return nil
}

// SuppressedByAdminMessage is the human sentence returned to a billing service
// whose push was accepted but not applied. Kept as a constant so the API, the
// audit reason and the tests all say the same thing.
const SuppressedByAdminMessage = "An admin override holds this organization's " +
	"entitlements; the push was recorded but not applied. Release the override to " +
	"let billing drive the limits again."

// suppressionReason keeps the caller's own reason when it sent one and falls
// back to the standard sentence otherwise, so the audit row is never mute
// about why nothing changed.
func suppressionReason(reason string) *string {
	out := SuppressedByAdminMessage
	if reason != "" {
		out = reason + " — " + SuppressedByAdminMessage
	}

	return &out
}

// Release drops an org's stored entitlements row so billing (or, until billing
// pushes again, the deployment-mode defaults) drives the limits once more.
//
// Deleting rather than rewriting is deliberate: a released org must resolve to
// exactly what a never-configured org resolves to, and keeping a husk row with
// a different source would leave a second thing to reason about.
//
// Reports whether a row was actually removed — releasing an org that has no
// override is a no-op, not an error, so a double-click cannot fail.
func (s *Service) Release(ctx context.Context, orgUID, actor, reason string) (bool, error) {
	previous, err := s.db.GetOrgEntitlements(ctx, orgUID)
	if err != nil {
		return false, fmt.Errorf("load previous entitlements: %w", err)
	}

	if previous == nil {
		return false, nil
	}

	before, err := modelToJSON(previous)
	if err != nil {
		return false, fmt.Errorf("snapshot before: %w", err)
	}

	var reasonPtr *string
	if reason != "" {
		r := reason
		reasonPtr = &r
	}

	audit := models.NewOrgEntitlementAudit(
		orgUID, AuditSourceAdminReleased, actor, before, models.JSONMap{"released": true}, reasonPtr,
	)

	if err := s.db.DeleteOrgEntitlements(ctx, orgUID, audit); err != nil {
		return false, fmt.Errorf("delete entitlements: %w", err)
	}

	// Same cache invalidation Set performs: the org's rate limiter was built
	// from the now-deleted cap and would otherwise outlive it.
	s.limitersMu.Lock()
	delete(s.limiters, orgUID)
	s.limitersMu.Unlock()

	// Back to the free tier for the scheduler — a released org has no plan.
	s.denormalizePlanWeight(ctx, orgUID, models.EntitlementSourceDefault)

	slog.InfoContext(ctx, "entitlements released to billing",
		"orgUID", orgUID, "actor", actor)

	return true, nil
}
