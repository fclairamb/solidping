// Package manifestapply reconciles a whole config-as-code manifest: checks,
// notification channels, maintenance windows and status pages (spec
// 2026-11-21-01). Checks keep their own reconciler (checks.Service.ApplyChecks);
// this package plans and applies the other kinds in dependency order around it,
// reusing each service's create/update/delete.
package manifestapply

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
	"github.com/fclairamb/solidping/server/internal/handlers/maintenancewindows"
	"github.com/fclairamb/solidping/server/internal/handlers/statuspages"
	"github.com/fclairamb/solidping/server/internal/secretref"
)

var (
	// ErrInvalidManifest wraps every manifest-content refusal (400).
	ErrInvalidManifest = errors.New("invalid manifest")
	// ErrUnknownCheckRef is a window or status page naming a check that is
	// neither in the manifest nor in the organization.
	ErrUnknownCheckRef = errors.New("unknown check reference")
)

// slugPattern is the shape of a manifest key for channels and windows.
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,98}$`)

// op is one planned mutation. do is nil for entries that write nothing
// (unchanged, and unmanaged-skipped ones never exist: unmanaged is adopted).
type op struct {
	entry checks.ApplyPlanEntry
	do    func(ctx context.Context) error
}

// kindPlan is the plan of one resource kind: upserts in manifest order, then
// the delete-by-absence entries.
type kindPlan struct {
	upserts []op
	deletes []op
}

// Service is the multi-kind apply orchestrator.
type Service struct {
	db           db.Service
	checks       *checks.Service
	integrations *integrations.Service
	windows      *maintenancewindows.Service
	pages        *statuspages.Service
}

// NewService builds the orchestrator over the existing per-kind services.
func NewService(
	dbService db.Service, checksSvc *checks.Service, integrationsSvc *integrations.Service,
	windowsSvc *maintenancewindows.Service, pagesSvc *statuspages.Service,
) *Service {
	return &Service{
		db: dbService, checks: checksSvc, integrations: integrationsSvc,
		windows: windowsSvc, pages: pagesSvc,
	}
}

// refSets are the check slugs a window or page may reference.
type refSets struct {
	slugToUID map[string]string
	uidToSlug map[string]string
	manifest  map[string]bool
}

func (r *refSets) known(slug string) bool {
	_, exists := r.slugToUID[slug]

	return exists || r.manifest[slug]
}

func (s *Service) loadRefs(ctx context.Context, orgUID string, doc *checks.ExportDocument) (*refSets, error) {
	rows, _, err := s.db.ListChecks(ctx, orgUID, &models.ListChecksFilter{})
	if err != nil {
		return nil, fmt.Errorf("list checks: %w", err)
	}

	refs := &refSets{
		slugToUID: make(map[string]string, len(rows)),
		uidToSlug: make(map[string]string, len(rows)),
		manifest:  make(map[string]bool, len(doc.Checks)),
	}
	for _, row := range rows {
		if row.Slug == nil {
			continue
		}

		refs.slugToUID[*row.Slug] = row.UID
		refs.uidToSlug[row.UID] = *row.Slug
	}

	for i := range doc.Checks {
		refs.manifest[doc.Checks[i].Slug] = true
	}

	return refs, nil
}

// resolveCheckUIDs maps check slugs to UIDs at execution time (the checks of
// the same manifest exist by then).
func (s *Service) resolveCheckUIDs(ctx context.Context, orgUID string, slugs []string) ([]string, error) {
	uids := make([]string, 0, len(slugs))
	for _, slug := range slugs {
		check, err := s.db.GetCheckByUidOrSlug(ctx, orgUID, slug)
		if err != nil || check == nil {
			return nil, fmt.Errorf("%w: %q", ErrUnknownCheckRef, slug)
		}

		uids = append(uids, check.UID)
	}

	return uids, nil
}

// Apply reconciles the manifest. Order: checks, channels, maintenance windows,
// status pages; prune runs in the reverse dependency order for the non-check
// kinds (a check prune is part of the checks step). A kind whose array is empty
// or absent is not reconciled at all, so it is never pruned.
//
//nolint:cyclop,funlen // plan → cap guard → apply → prune → audit
func (s *Service) Apply(
	ctx context.Context, orgSlug string, doc *checks.ExportDocument, opts checks.ApplyOptions,
) (*checks.ApplyResult, error) {
	if doc.IsEmpty() {
		return nil, checks.ErrEmptyChecksArray
	}

	if doc.Version != checks.ExportVersionV1 && doc.Version != checks.ExportVersionV2 {
		return nil, checks.ErrUnsupportedExportVersion
	}

	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, checks.ErrOrganizationNotFound
	}

	manifest := checks.ManifestName(doc, orgSlug)

	refs, err := s.loadRefs(ctx, org.UID, doc)
	if err != nil {
		return nil, err
	}

	resolver := secretref.DocumentResolver(s.db, org.UID, s.checks.EnvRefAllow())

	chanPlan, err := s.planChannels(ctx, org, doc, manifest, resolver)
	if err != nil {
		return nil, err
	}

	winPlan, err := s.planWindows(ctx, org, doc, manifest, refs)
	if err != nil {
		return nil, err
	}

	pagePlan, err := s.planPages(ctx, org, doc, manifest, refs)
	if err != nil {
		return nil, err
	}

	// Checks: always a dry run first when something else rides along, so the
	// deletion cap covers every kind and a refusal happens before any write.
	var checksRes *checks.ApplyResult

	if len(doc.Checks) > 0 && (opts.DryRun || !doc.HasOnlyChecks()) {
		dry := opts
		dry.DryRun = true

		checksRes, err = s.checks.ApplyChecks(ctx, orgSlug, doc, dry)
		if err != nil {
			return nil, err
		}
	}

	total := 0
	if checksRes != nil && opts.Prune {
		total += checksRes.Deleted
	}
	if opts.Prune {
		total += len(chanPlan.deletes) + len(winPlan.deletes) + len(pagePlan.deletes)
	}

	capLimit := opts.DeletionCap
	if capLimit <= 0 {
		capLimit = checks.DefaultDeletionCap
	}
	if opts.Prune && !opts.Force && total > capLimit {
		return nil, fmt.Errorf("%w: %d deletes requested, cap is %d (use force to override)",
			checks.ErrDeletionCapExceeded, total, capLimit)
	}

	result := &checks.ApplyResult{
		Manifest: manifest, DryRun: opts.DryRun, Pruned: opts.Prune,
		Errors: []checks.ImportError{}, Warnings: []string{}, Plan: []checks.ApplyPlanEntry{},
	}

	if len(doc.Checks) > 0 {
		if !opts.DryRun {
			checksRes, err = s.checks.ApplyChecks(ctx, orgSlug, doc, opts)
			if err != nil {
				return nil, err
			}
		}

		mergeChecks(result, checksRes)
	}

	for _, plan := range []*kindPlan{chanPlan, winPlan, pagePlan} {
		s.runUpserts(ctx, result, plan, opts.DryRun)
	}

	// Prune in reverse dependency order.
	for _, plan := range []*kindPlan{pagePlan, winPlan, chanPlan} {
		s.runDeletes(ctx, result, plan, opts)
	}

	if !opts.DryRun {
		audit.Record(ctx, s.db, org.UID, models.EventTypeConfigApplied,
			audit.Target{Type: "manifest", Name: manifest},
			models.JSONMap{
				"manifest":  manifest,
				"kinds":     kindsOf(doc),
				"created":   result.Created,
				"updated":   result.Updated,
				"unchanged": result.Unchanged,
				"deleted":   result.Deleted,
				"unmanaged": result.Unmanaged,
				"pruned":    opts.Prune,
				"forced":    opts.Force,
				"errors":    len(result.Errors),
			})
	}

	return result, nil
}

func kindsOf(doc *checks.ExportDocument) []string {
	kinds := []string{}
	if len(doc.Checks) > 0 {
		kinds = append(kinds, checks.KindCheck)
	}
	if len(doc.Channels) > 0 {
		kinds = append(kinds, checks.KindChannel)
	}
	if len(doc.MaintenanceWindows) > 0 {
		kinds = append(kinds, checks.KindMaintenanceWindow)
	}
	if len(doc.StatusPages) > 0 {
		kinds = append(kinds, checks.KindStatusPage)
	}

	return kinds
}

func mergeChecks(result, checksRes *checks.ApplyResult) {
	result.Created += checksRes.Created
	result.Updated += checksRes.Updated
	result.Unchanged += checksRes.Unchanged
	result.Deleted += checksRes.Deleted
	result.Unmanaged += checksRes.Unmanaged
	result.Plan = append(result.Plan, checksRes.Plan...)
	result.Warnings = append(result.Warnings, checksRes.Warnings...)
	result.Errors = append(result.Errors, checksRes.Errors...)
}

// runUpserts records the plan entries and, unless dry, performs the writes.
func (s *Service) runUpserts(ctx context.Context, result *checks.ApplyResult, plan *kindPlan, dry bool) {
	for i := range plan.upserts {
		entry := plan.upserts[i].entry
		result.Plan = append(result.Plan, entry)

		switch entry.Action {
		case checks.ApplyActionUnchanged:
			result.Unchanged++

			continue
		case checks.ApplyActionUnmanaged:
			result.Unmanaged++
		}

		if dry {
			switch entry.Action {
			case checks.ApplyActionCreate:
				result.Created++
			case checks.ApplyActionUpdate, checks.ApplyActionUnmanaged:
				result.Updated++
			}

			continue
		}

		if err := plan.upserts[i].do(ctx); err != nil {
			result.Errors = append(result.Errors, checks.ImportError{
				Index: -1, Slug: entry.Slug, Error: entry.Kind + ": " + err.Error(),
			})

			continue
		}

		if entry.Action == checks.ApplyActionCreate {
			result.Created++
		} else {
			result.Updated++
		}
	}
}

// runDeletes records the delete entries and, when pruning for real, performs them.
func (s *Service) runDeletes(
	ctx context.Context, result *checks.ApplyResult, plan *kindPlan, opts checks.ApplyOptions,
) {
	if !opts.Prune {
		return
	}

	for i := range plan.deletes {
		entry := plan.deletes[i].entry
		result.Plan = append(result.Plan, entry)

		if opts.DryRun {
			result.Deleted++

			continue
		}

		if err := plan.deletes[i].do(ctx); err != nil {
			result.Errors = append(result.Errors, checks.ImportError{
				Index: -1, Slug: entry.Slug, Error: "prune delete: " + err.Error(),
			})

			continue
		}

		result.Deleted++
	}
}

// ptr returns a pointer to v.
func ptr[T any](v T) *T { return &v }

// sortedCopy returns a sorted copy of in.
func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)

	return out
}

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidManifest, fmt.Sprintf(format, args...))
}

// validateSlug checks a manifest key and its uniqueness within the kind.
func validateSlug(kind, slug string, seen map[string]bool) error {
	if !slugPattern.MatchString(slug) {
		return invalidf("%s slug %q must match %s", kind, slug, slugPattern.String())
	}

	if seen[slug] {
		return invalidf("%s slug %q appears twice", kind, slug)
	}

	seen[slug] = true

	return nil
}

// reasonUnmanaged explains an `unmanaged` plan entry.
const reasonUnmanaged = "slug exists without the managed marker for this manifest"

// classify turns the diff of a matched row into a plan entry: the row is
// `unmanaged` when this manifest does not own it (it is reported and adopted),
// `unchanged` when nothing differs, else `update`.
func classify(entry *checks.ApplyPlanEntry, managedBy *string, manifest string, changes []checks.CheckFieldChange) {
	switch {
	case !ownedBy(managedBy, manifest):
		entry.Action = checks.ApplyActionUnmanaged
		entry.Reason = reasonUnmanaged
		entry.Changes = changes
	case len(changes) == 0:
		entry.Action = checks.ApplyActionUnchanged
	default:
		entry.Action = checks.ApplyActionUpdate
		entry.Changes = changes
	}
}

func ownedBy(managedBy *string, manifest string) bool {
	return managedBy != nil && *managedBy == manifest
}

func deref(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

// deleteOps builds the delete-by-absence entries of one kind: rows owned by the
// manifest whose slug it no longer lists, sorted by slug.
func deleteOps[T any](
	kind, reason, manifest string, rows map[string]T, seen map[string]bool,
	managedBy func(T) *string, uid func(T) string, remove func(ctx context.Context, uid string) error,
) []op {
	var ops []op

	for slug, row := range rows {
		if !ownedBy(managedBy(row), manifest) || seen[slug] {
			continue
		}

		rowUID := uid(row)
		ops = append(ops, op{
			entry: checks.ApplyPlanEntry{Kind: kind, Slug: slug, Action: checks.ApplyActionDelete, Reason: reason},
			do:    func(ctx context.Context) error { return remove(ctx, rowUID) },
		})
	}

	sort.Slice(ops, func(a, b int) bool { return ops[a].entry.Slug < ops[b].entry.Slug })

	return ops
}
