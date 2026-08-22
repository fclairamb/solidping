package checks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ManagedLabelKey is the reserved label that marks a check as owned by a
// config-as-code manifest. Its value is the manifest name (defaults to the
// org slug). Reconcile (delete-by-absence) only ever touches checks carrying
// this label with the matching value — hand-created checks are never adopted
// or deleted.
const ManagedLabelKey = "solidping.io/managed"

// DefaultDeletionCap is the maximum number of managed checks a single apply
// will delete-by-absence without an explicit force opt-in. A bad manifest
// can't silently wipe a fleet.
const DefaultDeletionCap = 10

// Apply plan actions.
const (
	ApplyActionCreate    = "create"
	ApplyActionUpdate    = "update"
	ApplyActionRename    = "rename"
	ApplyActionDelete    = "delete"
	ApplyActionUnmanaged = "unmanaged"
)

var (
	// ErrDeletionCapExceeded is returned when an apply with prune would delete
	// more managed checks than the configured cap allows (without force).
	ErrDeletionCapExceeded = errors.New("deletion cap exceeded")
	// ErrUnresolvedSecretRef is returned when a ${env:…}/${param:…} reference
	// cannot be resolved at apply time.
	ErrUnresolvedSecretRef = errors.New("unresolved secret reference")
)

// secretRefPattern matches ${env:NAME} and ${param:KEY} references inside
// config string values. The scheme is env|param; the name is everything up to
// the closing brace.
var secretRefPattern = regexp.MustCompile(`\$\{(env|param):([^}]+)\}`)

// ApplyOptions controls a single apply run.
type ApplyOptions struct {
	// DryRun computes the plan and mutates nothing.
	DryRun bool
	// Prune enables delete-by-absence for managed checks absent from the file.
	Prune bool
	// Force lifts the deletion cap for this run.
	Force bool
	// DeletionCap is the max number of deletes allowed without Force. Zero
	// falls back to DefaultDeletionCap.
	DeletionCap int
}

// ApplyPlanEntry is one reconcile decision for a single slug.
type ApplyPlanEntry struct {
	Slug         string `json:"slug"`
	PreviousSlug string `json:"previousSlug,omitempty"`
	Action       string `json:"action"`
	Reason       string `json:"reason,omitempty"`
}

// ApplyResult is the extended import result returned by apply. It reports the
// full plan plus the create/update/delete/unmanaged counts and any warnings.
type ApplyResult struct {
	Manifest  string           `json:"manifest"`
	DryRun    bool             `json:"dryRun"`
	Pruned    bool             `json:"pruned"`
	Created   int              `json:"created"`
	Updated   int              `json:"updated"`
	Deleted   int              `json:"deleted"`
	Unmanaged int              `json:"unmanaged"`
	Plan      []ApplyPlanEntry `json:"plan"`
	Warnings  []string         `json:"warnings"`
	Errors    []ImportError    `json:"errors"`
}

// manifestName resolves the managed-label value for a document. The document's
// Organization field names the manifest; it falls back to the org slug so a
// plain export round-trips into a single managed scope.
func manifestName(doc *ExportDocument, orgSlug string) string {
	if doc.Organization != "" {
		return doc.Organization
	}

	return orgSlug
}

// computeApplyPlan diffs the manifest against the managed scope and returns the
// ordered plan. Pure with respect to the DB: it only reads. Matching is on
// slug within the managed-label scope; an explicit previousSlug (or uid) on a
// file check reconciles a rename in place.
//
//nolint:cyclop,funlen // single-pass reconcile over file + managed set
func (s *Service) computeApplyPlan(
	ctx context.Context, org *models.Organization, doc *ExportDocument, manifest string,
) ([]ApplyPlanEntry, error) {
	// Existing checks in the org, with their labels, so we can tell managed
	// from unmanaged and detect delete-by-absence.
	existing, _, err := s.db.ListChecks(ctx, org.UID, &models.ListChecksFilter{})
	if err != nil {
		return nil, fmt.Errorf("list checks for plan: %w", err)
	}

	uids := make([]string, 0, len(existing))
	slugByUID := make(map[string]string, len(existing))
	for _, c := range existing {
		uids = append(uids, c.UID)
		if c.Slug != nil {
			slugByUID[c.UID] = *c.Slug
		}
	}

	labelsByUID, err := s.db.GetLabelsForChecks(ctx, uids)
	if err != nil {
		return nil, fmt.Errorf("load labels for plan: %w", err)
	}

	// managedSlugs: slug -> true for checks carrying our managed label.
	managedSlugs := make(map[string]bool)
	existingSlugs := make(map[string]bool, len(existing))
	for uid, slug := range slugByUID {
		existingSlugs[slug] = true
		for _, lbl := range labelsByUID[uid] {
			if lbl.Key == ManagedLabelKey && lbl.Value == manifest {
				managedSlugs[slug] = true
			}
		}
	}

	plan := make([]ApplyPlanEntry, 0, len(doc.Checks))
	fileSlugs := make(map[string]bool, len(doc.Checks))

	for i := range doc.Checks {
		entry := &doc.Checks[i]
		fileSlugs[entry.Slug] = true

		// Rename: previousSlug present and refers to an existing managed check.
		prev := entry.PreviousSlug
		if prev != "" && prev != entry.Slug && existingSlugs[prev] {
			plan = append(plan, ApplyPlanEntry{
				Slug:         entry.Slug,
				PreviousSlug: prev,
				Action:       ApplyActionRename,
				Reason:       "previousSlug " + prev + " present in org",
			})
			// The previous slug is being consumed by the rename; mark it so the
			// delete-by-absence pass below doesn't also flag it.
			fileSlugs[prev] = true

			continue
		}

		switch {
		case !existingSlugs[entry.Slug]:
			plan = append(plan, ApplyPlanEntry{Slug: entry.Slug, Action: ApplyActionCreate})
		case managedSlugs[entry.Slug]:
			plan = append(plan, ApplyPlanEntry{Slug: entry.Slug, Action: ApplyActionUpdate})
		default:
			// Slug exists but is NOT managed by this manifest: report, never
			// auto-adopt. The apply will (re)stamp the managed label so a future
			// apply treats it as owned, but it is surfaced here for visibility.
			plan = append(plan, ApplyPlanEntry{
				Slug:   entry.Slug,
				Action: ApplyActionUnmanaged,
				Reason: "slug exists without the managed label for this manifest",
			})
		}
	}

	// Delete-by-absence: managed checks no longer present in the file.
	absent := make([]string, 0)
	for slug := range managedSlugs {
		if !fileSlugs[slug] {
			absent = append(absent, slug)
		}
	}
	sort.Strings(absent)
	for _, slug := range absent {
		plan = append(plan, ApplyPlanEntry{
			Slug:   slug,
			Action: ApplyActionDelete,
			Reason: "managed check absent from manifest",
		})
	}

	return plan, nil
}

// resolveSecretRefs walks every check's config, replacing ${env:NAME} and
// ${param:KEY} references with their resolved plaintext values. The resolved
// config is what feeds the existing upsert path, which envelopes secret fields
// into config_private. A missing reference is a hard error. When the
// credentials service is disabled (no master key) a resolved reference appends
// a warning (the secret will land in plaintext config). Mutates doc in place.
func (s *Service) resolveSecretRefs(
	ctx context.Context, orgUID string, doc *ExportDocument,
) ([]string, error) {
	var warnings []string

	resolvedAnySecret := false

	for idx := range doc.Checks {
		cfg := doc.Checks[idx].Config
		for key, val := range cfg {
			strVal, ok := val.(string)
			if !ok {
				continue
			}

			if !secretRefPattern.MatchString(strVal) {
				continue
			}

			resolved, didResolve, err := s.resolveRefString(ctx, orgUID, strVal)
			if err != nil {
				return nil, fmt.Errorf("check %q config %q: %w", doc.Checks[idx].Slug, key, err)
			}

			cfg[key] = resolved
			if didResolve {
				resolvedAnySecret = true
			}
		}
	}

	if resolvedAnySecret && !s.creds.Enabled() {
		warnings = append(warnings,
			"SP_ENCRYPTION_MASTER_KEY is unset: resolved secret references are stored in plaintext config")
	}

	return warnings, nil
}

// resolveRefString replaces every ${env:…}/${param:…} reference in a single
// string. Returns (resolved, anyResolved, error). A reference that can't be
// resolved is a hard error.
func (s *Service) resolveRefString(
	ctx context.Context, orgUID, input string,
) (string, bool, error) {
	var resolveErr error

	resolvedAny := false

	out := secretRefPattern.ReplaceAllStringFunc(input, func(match string) string {
		if resolveErr != nil {
			return match
		}

		groups := secretRefPattern.FindStringSubmatch(match)
		scheme, name := groups[1], groups[2]

		value, err := s.resolveRef(ctx, orgUID, scheme, name)
		if err != nil {
			resolveErr = err

			return match
		}

		resolvedAny = true

		return value
	})

	if resolveErr != nil {
		return "", false, resolveErr
	}

	return out, resolvedAny, nil
}

// resolveRef resolves a single env|param reference to its plaintext value.
func (s *Service) resolveRef(ctx context.Context, orgUID, scheme, name string) (string, error) {
	switch scheme {
	case "env":
		val, ok := os.LookupEnv(name)
		if !ok {
			return "", fmt.Errorf("%w: env:%s is not set", ErrUnresolvedSecretRef, name)
		}

		return val, nil
	case "param":
		return s.resolveParamRef(ctx, orgUID, name)
	default:
		return "", fmt.Errorf("%w: unknown scheme %q", ErrUnresolvedSecretRef, scheme)
	}
}

// resolveParamRef resolves a ${param:KEY} reference. Org-scoped parameters take
// precedence over system-wide ones. Parameter values are stored as
// {"value": <v>} JSONB; the secret flag only masks API responses, so the
// plaintext value is read directly here.
func (s *Service) resolveParamRef(ctx context.Context, orgUID, key string) (string, error) {
	if orgParam, err := s.db.GetOrgParameter(ctx, orgUID, key); err == nil && orgParam != nil {
		if v, ok := paramStringValue(orgParam); ok {
			return v, nil
		}
	}

	if sysParam, err := s.db.GetSystemParameter(ctx, key); err == nil && sysParam != nil {
		if v, ok := paramStringValue(sysParam); ok {
			return v, nil
		}
	}

	return "", fmt.Errorf("%w: param:%s not found", ErrUnresolvedSecretRef, key)
}

// paramStringValue extracts the stringified value from a parameter's
// {"value": …} envelope.
func paramStringValue(param *models.Parameter) (string, bool) {
	raw, ok := param.Value["value"]
	if !ok {
		return "", false
	}

	switch val := raw.(type) {
	case string:
		return val, true
	case fmt.Stringer:
		return val.String(), true
	default:
		return fmt.Sprintf("%v", val), true
	}
}

// ApplyChecks reconciles the manifest against the managed scope. With DryRun it
// computes the plan and mutates nothing. Otherwise it creates/updates via the
// existing upsert path (stamping the managed label on every owned check) and,
// when Prune is set, deletes managed checks absent from the file — gated by the
// deletion cap unless Force.
//
//nolint:cyclop,funlen // orchestrates plan → resolve → mutate → prune
func (s *Service) ApplyChecks(
	ctx context.Context, orgSlug string, doc *ExportDocument, opts ApplyOptions,
) (*ApplyResult, error) {
	if !isSupportedExportVersion(doc.Version) {
		return nil, ErrUnsupportedExportVersion
	}

	if len(doc.Checks) == 0 {
		return nil, ErrEmptyChecksArray
	}

	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	manifest := manifestName(doc, orgSlug)

	result := &ApplyResult{
		Manifest: manifest,
		DryRun:   opts.DryRun,
		Pruned:   opts.Prune,
		Errors:   []ImportError{},
		Warnings: []string{},
		Plan:     []ApplyPlanEntry{},
	}

	// Resolve secret references first so the plan reflects the real config and a
	// missing reference fails before any mutation.
	warnings, err := s.resolveSecretRefs(ctx, org.UID, doc)
	if err != nil {
		return nil, err
	}
	result.Warnings = append(result.Warnings, warnings...)

	plan, err := s.computeApplyPlan(ctx, org, doc, manifest)
	if err != nil {
		return nil, err
	}
	result.Plan = plan

	for i := range plan {
		switch plan[i].Action {
		case ApplyActionCreate, ApplyActionRename:
			result.Created++
		case ApplyActionUpdate:
			result.Updated++
		case ApplyActionUnmanaged:
			result.Unmanaged++
		case ApplyActionDelete:
			// counted in the prune section below
		}
	}

	// Deletion cap guard (applies even on dry-run so the operator sees the
	// refusal before they commit to the apply).
	deletes := s.pruneTargets(plan, opts)
	capLimit := opts.DeletionCap
	if capLimit <= 0 {
		capLimit = DefaultDeletionCap
	}
	if opts.Prune && !opts.Force && len(deletes) > capLimit {
		return nil, fmt.Errorf("%w: %d deletes requested, cap is %d (use force to override)",
			ErrDeletionCapExceeded, len(deletes), capLimit)
	}

	if opts.DryRun {
		// On dry-run, report what would be deleted so the count is meaningful.
		if opts.Prune {
			result.Deleted = len(deletes)
		}

		return result, nil
	}

	// Stamp the managed label on every owned check so future applies recognize
	// the scope. Done by injecting the label into each check's Labels map before
	// the upsert path persists it.
	s.stampManagedLabels(doc, manifest)

	// Create/update via the existing import path (handles groups, config,
	// labels, and dependsOn in two passes).
	importResult, err := s.ImportChecks(ctx, orgSlug, doc, false)
	if err != nil {
		return nil, err
	}
	result.Created = importResult.Created
	result.Updated = importResult.Updated
	result.Errors = append(result.Errors, importResult.Errors...)

	// Prune managed, absent checks.
	if opts.Prune {
		for _, slug := range deletes {
			if delErr := s.DeleteCheck(ctx, orgSlug, slug); delErr != nil {
				result.Errors = append(result.Errors, ImportError{
					Index: -1, Slug: slug, Error: "prune delete: " + delErr.Error(),
				})

				continue
			}

			result.Deleted++
		}
	}

	// One config.applied event per apply, carrying the SUMMARY COUNTS and the
	// manifest name — never the manifest body. A config-as-code document is
	// exactly where secret references and check credentials live, so storing
	// it would defeat the whole redaction rule; the counts are what an auditor
	// asks about ("what changed in the 09:14 apply?") and the per-check
	// check.created/updated/deleted events already carry the detail.
	audit.Record(ctx, s.db, org.UID, models.EventTypeConfigApplied,
		audit.Target{Type: "manifest", Name: manifest},
		models.JSONMap{
			"manifest":  manifest,
			"created":   result.Created,
			"updated":   result.Updated,
			"deleted":   result.Deleted,
			"unmanaged": result.Unmanaged,
			"pruned":    opts.Prune,
			"forced":    opts.Force,
			"errors":    len(result.Errors),
		})

	return result, nil
}

// pruneTargets returns the slugs the plan marks for delete-by-absence. Empty
// when prune is off.
func (s *Service) pruneTargets(plan []ApplyPlanEntry, opts ApplyOptions) []string {
	if !opts.Prune {
		return nil
	}

	targets := make([]string, 0)
	for i := range plan {
		if plan[i].Action == ApplyActionDelete {
			targets = append(targets, plan[i].Slug)
		}
	}

	return targets
}

// stampManagedLabels injects the reserved managed label into every check's
// Labels map so the upsert path persists it. Rename entries also clear stale
// managed ownership of the previous slug by leaving it for the prune pass — but
// since a rename consumes previousSlug, no prune happens for it.
func (s *Service) stampManagedLabels(doc *ExportDocument, manifest string) {
	for i := range doc.Checks {
		if doc.Checks[i].Labels == nil {
			doc.Checks[i].Labels = make(map[string]string, 1)
		}
		doc.Checks[i].Labels[ManagedLabelKey] = manifest
	}
}

// ParseManifest unmarshals a manifest body that may be JSON or YAML into an
// ExportDocument. YAML is converted to JSON first so the struct's json tags are
// honored uniformly.
func ParseManifest(body []byte, contentType string) (*ExportDocument, error) {
	trimmed := strings.TrimLeft(string(body), " \t\r\n")

	isJSON := strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") ||
		strings.Contains(contentType, "json")

	var doc ExportDocument
	if isJSON {
		if err := json.Unmarshal(body, &doc); err != nil {
			return nil, fmt.Errorf("parse json manifest: %w", err)
		}

		return &doc, nil
	}

	jsonBytes, err := yamlToJSON(body)
	if err != nil {
		return nil, fmt.Errorf("parse yaml manifest: %w", err)
	}

	if err := json.Unmarshal(jsonBytes, &doc); err != nil {
		return nil, fmt.Errorf("parse yaml manifest: %w", err)
	}

	return &doc, nil
}

// yamlToJSON converts a YAML document into equivalent JSON bytes so a struct's
// json tags drive the unmarshal. yaml.v3 decodes mappings into
// map[string]interface{}, which json.Marshal handles directly.
func yamlToJSON(body []byte) ([]byte, error) {
	var raw any
	if err := yaml.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode yaml: %w", err)
	}

	jsonBytes, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("re-encode as json: %w", err)
	}

	return jsonBytes, nil
}
