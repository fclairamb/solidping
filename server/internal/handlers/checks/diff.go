package checks

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/secretref"
)

// Reconcile actions shared by /import and /apply. `create`, `update`,
// `delete` and `unmanaged` predate spec 2026-09-11-04; `unchanged` is the one
// it adds, and it is the whole point: an import of a file that is
// byte-for-byte the current export used to answer `updated=482`, so the single
// question config-as-code exists to answer — *does the file match the
// instance?* — had no answer short of a client-side diff.
const (
	// ActionCreate: the slug is in the document and not in the organization.
	ActionCreate = ApplyActionCreate
	// ActionUpdate: the slug is in both and at least one field differs.
	ActionUpdate = ApplyActionUpdate
	// ActionUnchanged: the slug is in both and the NORMALIZED EFFECTIVE state
	// is identical, so writing it would change nothing.
	ActionUnchanged = "unchanged"
	// ActionDelete: a managed check absent from the document (apply + prune).
	ActionDelete = ApplyActionDelete
	// ActionUnmanaged: the slug exists but is not owned by this manifest.
	ActionUnmanaged = ApplyActionUnmanaged
)

// maskedValue is what a field diff prints instead of a secret or a
// reference-derived value. A plan is printed in CI logs and pasted into
// tickets; it must never be the thing that publishes a credential.
const maskedValue = "***"

// CheckFieldChange is one field an update would change, with the values
// rendered as strings (JSON for anything structured) so the plan is directly
// printable. Secret-bearing and reference-derived values are masked.
type CheckFieldChange struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// diffOptions tunes the comparison for the endpoint doing it.
type diffOptions struct {
	// IgnoreManagedLabel drops ManagedLabelKey from the label comparison.
	// True for /apply, which stamps that label itself AFTER the plan is
	// computed — without this, every managed check would report a label
	// change that apply immediately makes true. False for /import, which
	// stamps nothing, so there the label is ordinary user data.
	IgnoreManagedLabel bool
}

// orgCheckSnapshot is the organization's current check set, projected into the
// SAME ExportCheck shape a document carries, plus the underlying rows.
//
// Projecting through the exporter's own code (projectChecksToExport) is what
// makes "a fresh export dry-runs as unchanged" true by construction rather
// than by two implementations happening to agree: whatever the exporter emits
// for a check is exactly what the differ compares the document against.
type orgCheckSnapshot struct {
	current map[string]*ExportCheck
	rows    map[string]*models.Check
	labels  map[string][]*models.Label
}

// lookup returns the projection and row for a slug, or (nil, nil).
func (snap *orgCheckSnapshot) lookup(slug string) (*ExportCheck, *models.Check) {
	if snap == nil {
		return nil, nil
	}

	return snap.current[slug], snap.rows[slug]
}

// loadOrgCheckSnapshot reads the org's whole check set — rows, labels, group
// names and dependency edges — and projects it. One query set per import/apply
// run, never one per check.
func (s *Service) loadOrgCheckSnapshot(ctx context.Context, orgUID string) (*orgCheckSnapshot, error) {
	rows, _, err := s.db.ListChecks(ctx, orgUID, &models.ListChecksFilter{})
	if err != nil {
		return nil, fmt.Errorf("list checks for plan: %w", err)
	}

	labelsMap, groupMap, depsByChild, err := s.loadExportSidecars(ctx, orgUID, rows)
	if err != nil {
		return nil, err
	}

	projected := projectChecksToExport(rows, labelsMap, groupMap, depsByChild)

	snap := &orgCheckSnapshot{
		current: make(map[string]*ExportCheck, len(projected)),
		rows:    make(map[string]*models.Check, len(rows)),
		labels:  labelsMap,
	}

	for i := range projected {
		if projected[i].Slug != "" {
			snap.current[projected[i].Slug] = &projected[i]
		}
	}

	for _, row := range rows {
		if row.Slug != nil && *row.Slug != "" {
			snap.rows[*row.Slug] = row
		}
	}

	return snap, nil
}

// diffCheck reports every field an upsert of `desired` would change on the
// stored check `existing` (projected as `current`). An empty result is the
// definition of `unchanged`.
//
// It mirrors the WRITE semantics rather than comparing the two documents
// naively, because the two are not the same question:
//
//   - regions and group are compared only when the document names them — the
//     upsert leaves both alone otherwise;
//   - `escalationThreshold` is never compared: UpsertCheckRequest does not
//     carry it, so an import cannot change it;
//   - dependsOn is additive (import pass 2 merges), so only an edge the
//     document adds or re-kinds counts;
//   - a nil alerting pointer means "no opinion", never "reset to zero".
//
//nolint:cyclop,funlen // one linear comparison per field; splitting hides the set
func (s *Service) diffCheck(
	ctx context.Context,
	org *models.Organization,
	existing *models.Check,
	current, desired *ExportCheck,
	opts diffOptions,
) []CheckFieldChange {
	changes := make([]CheckFieldChange, 0, 4)

	add := func(field, from, to string) {
		if from != to {
			changes = append(changes, CheckFieldChange{Field: field, From: from, To: to})
		}
	}

	add(fieldName, current.Name, desired.Name)
	add("description", current.Description, desired.Description)
	add(fieldType, current.Type, desired.Type)
	add("enabled", strconv.FormatBool(current.Enabled), strconv.FormatBool(desired.Enabled))
	add("tracerouteOnFailure",
		traceroutePolicyOrInherit(current.TracerouteOnFailure),
		traceroutePolicyOrInherit(desired.TracerouteOnFailure))

	if desired.Period != "" {
		currentSecs, _ := periodStringToSeconds(current.Period)
		if desiredSecs, err := periodStringToSeconds(desired.Period); err == nil {
			add(fieldPeriod,
				formatDurationSecondsCompact(currentSecs), formatDurationSecondsCompact(desiredSecs))
		}
	}

	// A document that names no group leaves the check's group alone
	// (CheckGroupUID stays nil on the upsert). Names are matched the way the
	// importer matches them: case-insensitively.
	if desired.Group != "" && !strings.EqualFold(desired.Group, current.Group) {
		add("group", current.Group, desired.Group)
	}

	for _, field := range []struct {
		name            string
		current, wanted *int
	}{
		{fieldConfirmationPeriodSeconds, current.ConfirmationPeriodSeconds, desired.ConfirmationPeriodSeconds},
		{fieldRecoveryPeriodSeconds, current.RecoveryPeriodSeconds, desired.RecoveryPeriodSeconds},
		{"reopenCooldownMultiplier", current.ReopenCooldownMultiplier, desired.ReopenCooldownMultiplier},
		{fieldFlappingWindowSeconds, current.FlappingWindowSeconds, desired.FlappingWindowSeconds},
		{fieldFlapBackoffFactor, current.FlapBackoffFactor, desired.FlapBackoffFactor},
		{fieldMaxRecoveryMultiplier, current.MaxRecoveryMultiplier, desired.MaxRecoveryMultiplier},
	} {
		if field.wanted == nil {
			continue
		}

		add(field.name, intPtrString(field.current), strconv.Itoa(*field.wanted))
	}

	if len(desired.Regions) > 0 {
		// ResolveRegionsForCheck is what folds the accepted long
		// "@org/location" spelling down to the stored, canonical "@location" —
		// the single biggest source of the 183 false "changes" this spec
		// exists to remove.
		if resolved, err := s.regions.ResolveRegionsForCheck(ctx, desired.Regions, org.UID); err == nil {
			add(fieldRegions, joinSortedRegions(current.Regions), joinSortedRegions(resolved))
		}
	}

	changes = append(changes, diffLabels(current.Labels, desired.Labels, opts)...)
	changes = append(changes, diffCheckConfig(existing, current, desired)...)
	changes = append(changes, diffDependsOn(current.DependsOn, desired.DependsOn)...)

	sort.SliceStable(changes, func(i, j int) bool { return changes[i].Field < changes[j].Field })

	return changes
}

// traceroutePolicyOrInherit normalizes the absent path-trace policy to the
// `inherit` the importer always sends explicitly.
func traceroutePolicyOrInherit(value string) string {
	if value == "" {
		return TraceroutePolicyInherit
	}

	return value
}

// intPtrString renders a nullable int for a field diff; an absent current
// value prints as empty rather than as a misleading "0".
func intPtrString(value *int) string {
	if value == nil {
		return ""
	}

	return strconv.Itoa(*value)
}

// joinSortedRegions renders a region set order-insensitively: the execution
// set is a set, and a reordering is not a change.
func joinSortedRegions(regions []string) string {
	out := append([]string(nil), regions...)
	sort.Strings(out)

	return strings.Join(out, ",")
}

// diffLabels compares the label sets. The upsert replaces labels wholesale
// (UpsertCheck forwards `&req.Labels`), so a key present on only one side is a
// change in either direction.
func diffLabels(current, desired map[string]string, opts diffOptions) []CheckFieldChange {
	keys := make(map[string]struct{}, len(current)+len(desired))
	for key := range current {
		keys[key] = struct{}{}
	}
	for key := range desired {
		keys[key] = struct{}{}
	}

	if opts.IgnoreManagedLabel {
		delete(keys, ManagedLabelKey)
	}

	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)

	changes := make([]CheckFieldChange, 0, len(ordered))

	for _, key := range ordered {
		if current[key] != desired[key] {
			changes = append(changes, CheckFieldChange{
				Field: fieldLabelsPrefix + key, From: current[key], To: desired[key],
			})
		}
	}

	return changes
}

// diffCheckConfig compares the two configs on the NORMALIZED EFFECTIVE shape:
// the document's config is put through the same normalization the write path
// applies (normalizeCheckConfig — e.g. HTTP's username/password → basicAuth
// fold), and both sides are then projected through the exporter's own stripper
// so they are compared on exactly what a `secrets: stripped` document can
// carry.
//
// Two consequences worth stating, because they are the difference between a
// plan that converges and one that does not:
//
//   - Non-secret keys replace wholesale (mergePatchConfig drops public keys
//     absent from the patch), so a key on either side alone is a change.
//   - A key the exporter STRIPS cannot be compared — the stored value lives in
//     an encrypted column a dry run must not open. A document that supplies
//     one is therefore reported as a masked change rather than silently
//     called unchanged. An export never carries such a key, so the round trip
//     is unaffected; a hand-written manifest that inlines a password is the
//     case this covers.
func diffCheckConfig(existing *models.Check, current, desired *ExportCheck) []CheckFieldChange {
	if desired.Config == nil {
		return nil
	}

	normalized, err := normalizeCheckConfig(desired.Type, desired.Config)
	if err != nil {
		// A config that cannot be normalized is reported by the validators;
		// diffing the raw map is the honest fallback rather than claiming
		// "unchanged".
		normalized = desired.Config
	}

	hidden := hiddenExportConfigKeys(existing.Type, existing.ConfigPrivateKeys)

	desiredPublic := make(map[string]any, len(normalized))
	changes := make([]CheckFieldChange, 0, 2)

	for key, value := range normalized {
		if _, isHidden := hidden[key]; isHidden {
			changes = append(changes, CheckFieldChange{
				Field: fieldConfigPrefix + key, From: maskedValue, To: maskedValue,
			})

			continue
		}

		desiredPublic[key] = value
	}

	keys := make(map[string]struct{}, len(current.Config)+len(desiredPublic))
	for key := range current.Config {
		keys[key] = struct{}{}
	}
	for key := range desiredPublic {
		keys[key] = struct{}{}
	}

	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)

	for _, key := range ordered {
		from, to := canonicalConfigValue(current.Config[key]), canonicalConfigValue(desiredPublic[key])
		if from == to {
			continue
		}

		changes = append(changes, CheckFieldChange{
			Field: fieldConfigPrefix + key,
			From:  maskReferences(from),
			To:    maskReferences(to),
		})
	}

	return changes
}

// hiddenExportConfigKeys is the set of config keys the exporter removes for a
// check of this type: the checker's declared secrets, the export-redacted
// fields (spec 2026-09-11-02), and whatever the row already advertises as
// private. Same set stripSecretKeysForExport computes, named so the differ and
// the exporter cannot drift.
func hiddenExportConfigKeys(checkType string, configPrivateKeys *string) map[string]struct{} {
	hidden := map[string]struct{}{}

	if cfg, ok := registry.ParseConfig(checkerdef.CheckType(checkType)); ok {
		for _, key := range credentials.SecretFieldsFor(cfg) {
			hidden[key] = struct{}{}
		}

		for _, key := range credentials.ExportRedactedFieldsFor(cfg) {
			hidden[key] = struct{}{}
		}
	}

	if configPrivateKeys != nil && *configPrivateKeys != "" {
		var privateKeys []string
		if err := json.Unmarshal([]byte(*configPrivateKeys), &privateKeys); err == nil {
			for _, key := range privateKeys {
				hidden[key] = struct{}{}
			}
		}
	}

	return hidden
}

// canonicalConfigValue renders a config value so a YAML integer and a JSON
// float that mean the same number compare equal. An absent key renders as the
// empty string, which no JSON value produces.
func canonicalConfigValue(value any) string {
	if value == nil {
		return ""
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}

	return string(encoded)
}

// maskReferences replaces any ${env:…}/${param:…} reference inside a rendered
// value with the mask. The reference NAME is not itself a secret, but the
// value it stands for is, and a plan that prints
// `body: "…password=${param:sso-authtest-password}"` next to a real one is one
// copy-paste away from a leak.
func maskReferences(rendered string) string {
	if !strings.Contains(rendered, "${") {
		return rendered
	}

	return secretref.Pattern.ReplaceAllString(rendered, maskedValue)
}

// diffDependsOn compares the dependency edges ADDITIVELY, which is what import
// pass 2 does: an edge in the document that the check does not carry (or
// carries with a different kind) is a change; an edge the check has and the
// document omits is not, because the import would not remove it.
func diffDependsOn(current, desired []ExportedDependency) []CheckFieldChange {
	if len(desired) == 0 {
		return nil
	}

	currentKinds := make(map[string]string, len(current))
	for i := range current {
		currentKinds[current[i].ParentSlug] = current[i].Kind
	}

	changes := make([]CheckFieldChange, 0, len(desired))

	for i := range desired {
		want := desired[i].Kind
		if want == "" {
			want = string(models.CheckDependencyKindHard)
		}

		have, present := currentKinds[desired[i].ParentSlug]
		if present && have == want {
			continue
		}

		changes = append(changes, CheckFieldChange{
			Field: "dependsOn." + desired[i].ParentSlug, From: have, To: want,
		})
	}

	sort.SliceStable(changes, func(i, j int) bool { return changes[i].Field < changes[j].Field })

	return changes
}
