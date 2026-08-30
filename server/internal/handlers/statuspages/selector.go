package statuspages

// Section selectors (spec 2026-08-29-11) — dynamic status page membership.
//
// A section may carry a SectionSelector ({"all":true} or {"labels":{k:v,...}}).
// The system then keeps that section's check resources in sync, so a check
// created after the page was built still shows up. The failure this exists to
// remove is silent: a new service ships, its check goes down, and the page
// stays GREEN because nobody remembered to attach it.
//
// Three design commitments are load-bearing and should not be "simplified":
//
//  1. MATERIALIZE, don't virtualize. The reconciler writes real
//     StatusPageResource rows flagged ManagedBySelector. Availability
//     enrichment, positions, the badge/summary/embed endpoints and
//     publications' affectedResources resolution all assume a real row; a
//     virtual membership would have to be re-implemented in each of them.
//  2. MANUAL WINS. A check that already sits on the page as a manual resource
//     is skipped by every selector, and manual rows are never written or
//     deleted by the reconciler. Ownership is decided by one boolean column,
//     not by inference.
//  3. BEST EFFORT, never transactional with the triggering write. A check
//     create must not fail because a status page reconcile did. The page-view
//     backstop is the safety net that stops drift from persisting.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/statuspagecache"
)

// selectorBackstopInterval bounds how often a public page view may trigger a
// backstop reconcile. It is deliberately equal to statuspagecache.PageMaxAge:
// the public payload is already served `public, max-age=60`, so reconciling
// more often than that could not make a change visible any sooner, and
// reconciling less often would let drift outlive the cache the reader is
// actually looking at.
const selectorBackstopInterval = statuspagecache.PageMaxAge

// maxManagedResourcesPerSection caps how many rows one selector may
// materialize into a single section.
//
// It is a blast-radius limit, not a performance tuning knob: `{"all":true}` in
// an org with thousands of checks would otherwise render one page with
// thousands of components, each carrying its own availability series. Above the
// cap the reconciler materializes the first N in the same stable alphabetical
// order and reports the overflow, so the section shows a deterministic prefix
// plus an explicit "and N more" rather than silently truncating or timing out.
const maxManagedResourcesPerSection = 200

// parseSelector strictly decodes a raw `selector` value from a request body.
//
// Strict on purpose: `{"lables": {...}}` decoded leniently is a selector that
// matches nothing, forever, silently — the exact silent-omission failure this
// feature exists to remove. A typo has to be a 400.
func parseSelector(raw json.RawMessage) (*models.SectionSelector, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil //nolint:nilnil // an explicit null clears the selector; not an error
	}

	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()

	var selector models.SectionSelector
	if err := decoder.Decode(&selector); err != nil {
		return nil, ErrSelectorInvalid
	}

	if err := selector.Validate(); err != nil {
		return nil, err
	}

	return &selector, nil
}

// --- Reconciliation ---

// selectorReconcileMarks records, per page UID, when the backstop last ran.
// Process-local by design: it only rate-limits an idempotent operation, so a
// second replica simply reconciles on its own schedule and converges to the
// same rows.
type selectorReconcileMarks struct {
	marks sync.Map // pageUID -> time.Time
}

// due reports whether the page is due for a backstop reconcile, and claims the
// slot if so.
func (m *selectorReconcileMarks) due(pageUID string, now time.Time) bool {
	if last, ok := m.marks.Load(pageUID); ok {
		if lastTime, castOK := last.(time.Time); castOK && now.Sub(lastTime) < selectorBackstopInterval {
			return false
		}
	}

	m.marks.Store(pageUID, now)

	return true
}

// invalidate forces the next view of the page to reconcile, whatever the
// interval says. Used when the page's own configuration changed.
func (m *selectorReconcileMarks) invalidate(pageUID string) {
	m.marks.Delete(pageUID)
}

// ReconcileOrgSelectors brings every selector-bearing section in the
// organization back in sync. Best-effort: it logs and swallows failures,
// because its callers are check writes that must succeed regardless.
//
// It is the entry point the checks handler calls after a check is created,
// updated (labels are replaced wholesale there) or deleted.
func (s *Service) ReconcileOrgSelectors(ctx context.Context, orgUID string) {
	// The contract this method advertises is "cannot fail the caller's write".
	// Swallowing errors only delivers that for errors; a nil map or a slice
	// bound anywhere below would still panic up through an already-committed
	// check create and surface as a 500 on a request that actually succeeded.
	// The recover closes that gap, and logs loudly rather than silently.
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.ErrorContext(ctx, "Panic while reconciling status page selectors",
				"panic", recovered, "orgUid", orgUID)
		}
	}()

	pageUIDs, err := s.db.ListSelectorSectionPageUIDs(ctx, orgUID)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to list selector-bearing status pages",
			"error", err, "orgUid", orgUID)

		return
	}

	for _, pageUID := range pageUIDs {
		s.reconcileMarks.invalidate(pageUID)

		if err := s.reconcilePage(ctx, orgUID, pageUID); err != nil {
			slog.ErrorContext(ctx, "Failed to reconcile status page selectors",
				"error", err, "orgUid", orgUID, "statusPageUid", pageUID)
		}
	}
}

// reconcilePageBestEffort reconciles one page and logs any failure. Used from
// the status page write paths, where the operator's own action must not fail
// because a reconcile did.
func (s *Service) reconcilePageBestEffort(ctx context.Context, orgUID, pageUID string) {
	s.reconcileMarks.invalidate(pageUID)

	if err := s.reconcilePage(ctx, orgUID, pageUID); err != nil {
		slog.ErrorContext(ctx, "Failed to reconcile status page selectors",
			"error", err, "orgUid", orgUID, "statusPageUid", pageUID)
	}
}

// maybeReconcileOnView is the backstop: a cheap, rate-limited reconcile on the
// read path so drift can never persist even if a write-path trigger was missed
// (a direct database edit, a crashed process, a replica that was down). At most
// one reconcile per page per selectorBackstopInterval.
func (s *Service) maybeReconcileOnView(ctx context.Context, orgUID, pageUID string) {
	if !s.reconcileMarks.due(pageUID, time.Now()) {
		return
	}

	if err := s.reconcilePage(ctx, orgUID, pageUID); err != nil {
		slog.ErrorContext(ctx, "Backstop reconcile of status page selectors failed",
			"error", err, "orgUid", orgUID, "statusPageUid", pageUID)
	}
}

// sectionState is one section plus the resources currently stored for it.
type sectionState struct {
	section   *models.StatusPageSection
	resources []*models.StatusPageResource
}

// loadPageState reads every live section of a page together with its resources,
// in section position order.
func (s *Service) loadPageState(ctx context.Context, pageUID string) ([]sectionState, error) {
	sections, err := s.db.ListStatusPageSections(ctx, pageUID)
	if err != nil {
		return nil, err
	}

	states := make([]sectionState, 0, len(sections))

	for _, section := range sections {
		resources, resErr := s.db.ListStatusPageResources(ctx, section.UID)
		if resErr != nil {
			return nil, resErr
		}

		states = append(states, sectionState{section: section, resources: resources})
	}

	return states, nil
}

// ReconcilePage brings one page's selector sections in sync. Exported for the
// tests that pin idempotence and ordering; production callers go through
// ReconcileOrgSelectors / reconcilePageBestEffort / maybeReconcileOnView.
func (s *Service) ReconcilePage(ctx context.Context, orgUID, pageUID string) error {
	return s.reconcilePage(ctx, orgUID, pageUID)
}

// reconcilePage is the idempotent core. Reconciling an unchanged page twice
// issues ZERO writes — that is what keeps public row order stable between two
// polls, and it is asserted by a test rather than assumed.
func (s *Service) reconcilePage(ctx context.Context, orgUID, pageUID string) error {
	states, err := s.loadPageState(ctx, pageUID)
	if err != nil {
		return err
	}

	if !needsReconcile(states) {
		return nil
	}

	// Manual placement wins, page-wide: a check an operator put somewhere by
	// hand is never duplicated by a selector, wherever on the page it sits.
	claimed := manualCheckUIDs(states)

	for i := range states {
		// A section whose selector was CLEARED still owns managed rows from
		// when it had one. Reconciling it against the empty desired set is
		// what removes them — skipping selector-less sections here would leave
		// a page advertising checks under a rule that no longer exists.
		if states[i].section.Selector == nil {
			if err := s.dropManagedRows(ctx, &states[i]); err != nil {
				return err
			}

			continue
		}

		if err := s.reconcileSection(ctx, orgUID, &states[i], claimed); err != nil {
			return err
		}
	}

	return nil
}

// needsReconcile reports whether the page has anything to reconcile — a
// dynamic section, or leftover managed rows from one that was cleared. A fully
// hand-curated page therefore costs one sections read and nothing else.
func needsReconcile(states []sectionState) bool {
	for i := range states {
		if states[i].section.Selector != nil {
			return true
		}

		for _, resource := range states[i].resources {
			if resource.ManagedBySelector {
				return true
			}
		}
	}

	return false
}

// dropManagedRows removes every selector-owned row from a section that has no
// selector any more. Manual rows in the same section are untouched.
func (s *Service) dropManagedRows(ctx context.Context, state *sectionState) error {
	for _, resource := range state.resources {
		if !resource.ManagedBySelector {
			continue
		}

		if err := s.db.DeleteStatusPageResource(ctx, resource.UID); err != nil {
			return err
		}
	}

	return nil
}

// manualCheckUIDs collects every check the operator placed by hand anywhere on
// the page. Group resources are irrelevant here: a selector only ever
// materializes check-type rows, and a group resource deliberately renders as
// one rolled-up component that no selector should second-guess.
func manualCheckUIDs(states []sectionState) map[string]struct{} {
	claimed := make(map[string]struct{})

	for i := range states {
		for _, resource := range states[i].resources {
			if resource.ManagedBySelector || resource.CheckUID == nil {
				continue
			}

			claimed[*resource.CheckUID] = struct{}{}
		}
	}

	return claimed
}

// reconcileSection syncs one selector section. `claimed` carries the checks
// already spoken for — manual rows page-wide, plus whatever earlier selector
// sections took — and is extended with this section's picks, so two overlapping
// selectors on one page produce no duplicate component: the earlier section
// (by position) wins, deterministically.
func (s *Service) reconcileSection(
	ctx context.Context, orgUID string, state *sectionState, claimed map[string]struct{},
) error {
	desired, err := s.desiredChecks(ctx, orgUID, state.section.Selector, claimed)
	if err != nil {
		return err
	}

	for _, checkUID := range desired {
		claimed[checkUID] = struct{}{}
	}

	existing := make(map[string]*models.StatusPageResource, len(state.resources))
	maxManualPosition := 0

	for _, resource := range state.resources {
		if !resource.ManagedBySelector {
			if resource.Position > maxManualPosition {
				maxManualPosition = resource.Position
			}

			continue
		}

		if resource.CheckUID != nil {
			existing[*resource.CheckUID] = resource
		}
	}

	wanted := make(map[string]struct{}, len(desired))
	for _, checkUID := range desired {
		wanted[checkUID] = struct{}{}
	}

	// Removals go through the SAME call a manual delete uses, so a check
	// dropping out of a selector is byte-for-byte the same event downstream as
	// an operator removing it by hand — including what a past publication's
	// affectedResources then renders.
	for checkUID, resource := range existing {
		if _, keep := wanted[checkUID]; keep {
			continue
		}

		if err := s.db.DeleteStatusPageResource(ctx, resource.UID); err != nil {
			return err
		}

		delete(existing, checkUID)
	}

	return s.materialize(ctx, state.section.UID, desired, existing, maxManualPosition)
}

// materialize inserts the missing managed rows and renumbers the managed rows
// so they follow the manual ones in the selector's stable order. Positions are
// written ONLY when they actually differ, so a no-op reconcile is a no-op at
// the database too.
func (s *Service) materialize(
	ctx context.Context,
	sectionUID string,
	desired []string,
	existing map[string]*models.StatusPageResource,
	maxManualPosition int,
) error {
	for i, checkUID := range desired {
		position := maxManualPosition + 1 + i

		resource, found := existing[checkUID]
		if !found {
			if err := s.db.CreateStatusPageResource(
				ctx, models.NewManagedStatusPageResource(sectionUID, checkUID, position),
			); err != nil {
				return err
			}

			continue
		}

		if resource.Position == position {
			continue
		}

		update := &models.StatusPageResourceUpdate{Position: &position}
		if err := s.db.UpdateStatusPageResource(ctx, resource.UID, update); err != nil {
			return err
		}
	}

	return nil
}

// desiredChecks resolves a selector to the ordered list of check UIDs it should
// materialize.
//
// Matching is delegated to ListChecks with the selector's own ListChecksFilter
// — the very query the checks list uses — so there is exactly one place where
// "does this check match these labels" is decided. That also inherits the
// filter's default of EXCLUDING internal checks, which is the behavior we
// want: an internal plumbing probe must never be swept onto a status page.
//
// The order is alphabetical by display name, with the UID as a final
// tiebreaker so equal names cannot shuffle between two reconciles.
func (s *Service) desiredChecks(
	ctx context.Context, orgUID string, selector *models.SectionSelector, claimed map[string]struct{},
) ([]string, error) {
	checks, _, err := s.db.ListChecks(ctx, orgUID, selector.Filter())
	if err != nil {
		return nil, err
	}

	type candidate struct {
		uid  string
		name string
	}

	candidates := make([]candidate, 0, len(checks))

	for _, check := range checks {
		if _, taken := claimed[check.UID]; taken {
			continue
		}

		candidates = append(candidates, candidate{uid: check.UID, name: checkSortName(check)})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].name != candidates[j].name {
			return candidates[i].name < candidates[j].name
		}

		return candidates[i].uid < candidates[j].uid
	})

	if len(candidates) > maxManagedResourcesPerSection {
		candidates = candidates[:maxManagedResourcesPerSection]
	}

	uids := make([]string, len(candidates))
	for i := range candidates {
		uids[i] = candidates[i].uid
	}

	return uids, nil
}

// checkSortName is the alphabetisation key: the check's display name, falling
// back to its slug for the rare unnamed check so the ordering is still total.
func checkSortName(check *models.Check) string {
	if check.Name != nil && *check.Name != "" {
		return strings.ToLower(*check.Name)
	}

	if check.Slug != nil {
		return strings.ToLower(*check.Slug)
	}

	return check.UID
}

// countSelectorMatches reports how many checks a selector matches in total,
// used to tell the dashboard how many rows the cap is hiding.
func (s *Service) countSelectorMatches(
	ctx context.Context, orgUID string, selector *models.SectionSelector,
) (int, error) {
	_, total, err := s.db.ListChecks(ctx, orgUID, selector.Filter())
	if err != nil {
		return 0, err
	}

	return int(total), nil
}

// selectorValidationError reports whether err is one of the selector
// validation failures, so the handler can answer VALIDATION_ERROR rather than
// 500.
func selectorValidationError(err error) bool {
	return errors.Is(err, ErrSelectorInvalid) ||
		errors.Is(err, models.ErrSelectorEmpty) ||
		errors.Is(err, models.ErrSelectorAmbiguous) ||
		errors.Is(err, models.ErrSelectorLabelsEmpty) ||
		errors.Is(err, models.ErrSelectorTooManyLabels) ||
		errors.Is(err, models.ErrSelectorLabelKeyInvalid) ||
		errors.Is(err, models.ErrSelectorLabelValueInvalid)
}

// dropManagedRowForCheck removes the selector-owned row for a check in one
// section, if there is one. Called before a MANUAL resource is inserted for
// the same check: the two cannot coexist (a partial unique index on
// (section_uid, check_uid) forbids it), and between the two the manual row is
// the one that wins.
func (s *Service) dropManagedRowForCheck(ctx context.Context, sectionUID, checkUID string) error {
	resources, err := s.db.ListStatusPageResources(ctx, sectionUID)
	if err != nil {
		return err
	}

	for _, resource := range resources {
		if !resource.ManagedBySelector || resource.CheckUID == nil || *resource.CheckUID != checkUID {
			continue
		}

		if errDelete := s.db.DeleteStatusPageResource(ctx, resource.UID); errDelete != nil {
			return errDelete
		}
	}

	return nil
}
