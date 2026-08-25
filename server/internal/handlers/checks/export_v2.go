package checks

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// Export document format versions. The exporter emits v2 only; the importer
// (and apply) accept both v1 and v2.
const (
	// ExportVersionV1 is the legacy per-row-faithful format: every alerting
	// field explicit per check, `period` as an HH:MM:SS clock string, other
	// durations as `...Seconds` integers, `enabled` on every check, and the
	// `_secretsStripped` boolean meta field.
	ExportVersionV1 = 1
	// ExportVersionV2 adds a top-level `defaults` block (document-wide modal
	// values with per-check overrides only), Go-style duration strings
	// everywhere, `disabled,omitempty` instead of `enabled`, deterministic
	// ordering, and the `secrets: "stripped"` marker.
	ExportVersionV2 = 2
)

// isSupportedExportVersion reports whether an import/apply document version is
// one this build understands.
func isSupportedExportVersion(v int) bool {
	return v == ExportVersionV1 || v == ExportVersionV2
}

// exportDocumentV2 is the JSON wire shape emitted by the v2 exporter. It is a
// projection of the canonical ExportDocument: durations are rendered as
// Go-style strings and per-check fields equal to the document default are
// omitted. It is never used as the in-memory representation — import decodes
// into the canonical ExportDocument (see ExportDocument.UnmarshalJSON).
type exportDocumentV2 struct {
	Version      int               `json:"version"`
	ExportedAt   string            `json:"exportedAt,omitempty"`
	Organization string            `json:"organization,omitempty"`
	Secrets      string            `json:"secrets,omitempty"`
	Defaults     *exportDefaultsV2 `json:"defaults,omitempty"`
	Checks       []exportCheckV2   `json:"checks"`
}

// exportDefaultsV2 holds the document-wide default for every field a check may
// override. Durations are Go-style strings; unitless counters stay integers.
type exportDefaultsV2 struct {
	Regions               []string `json:"regions,omitempty"`
	Period                string   `json:"period,omitempty"`
	ConfirmationPeriod    string   `json:"confirmationPeriod,omitempty"`
	EscalationThreshold   *int     `json:"escalationThreshold,omitempty"`
	RecoveryPeriod        string   `json:"recoveryPeriod,omitempty"`
	FlappingWindow        string   `json:"flappingWindow,omitempty"`
	FlapBackoffFactor     *int     `json:"flapBackoffFactor,omitempty"`
	MaxRecoveryMultiplier *int     `json:"maxRecoveryMultiplier,omitempty"`
}

// exportCheckV2 is one check in the v2 wire shape. Every defaulted field is
// omitempty: it appears only when it differs from the document default.
type exportCheckV2 struct {
	Name                string            `json:"name,omitempty"`
	Slug                string            `json:"slug"`
	PreviousSlug        string            `json:"previousSlug,omitempty"`
	Description         string            `json:"description,omitempty"`
	Type                string            `json:"type"`
	Config              map[string]any    `json:"config"`
	Group               string            `json:"group,omitempty"`
	Labels              map[string]string `json:"labels,omitempty"`
	Regions             []string          `json:"regions,omitempty"`
	Disabled            bool              `json:"disabled,omitempty"`
	Internal            bool              `json:"internal,omitempty"`
	Period              string            `json:"period,omitempty"`
	ConfirmationPeriod  string            `json:"confirmationPeriod,omitempty"`
	EscalationThreshold *int              `json:"escalationThreshold,omitempty"`
	RecoveryPeriod      string            `json:"recoveryPeriod,omitempty"`
	// TracerouteOnFailure is `on` or `off`; absent means `inherit` (spec
	// 2026-08-21-10). DELIBERATELY NOT part of the defaults block: it is a
	// policy an operator sets on the few checks that need it, so a modal
	// default would either be noise on every document or, worse, silently
	// re-derive an `off` into an `inherit` on a document where `off` happened
	// to be the majority.
	TracerouteOnFailure      string               `json:"tracerouteOnFailure,omitempty"`
	ReopenCooldownMultiplier *int                 `json:"reopenCooldownMultiplier,omitempty"`
	FlappingWindow           string               `json:"flappingWindow,omitempty"`
	FlapBackoffFactor        *int                 `json:"flapBackoffFactor,omitempty"`
	MaxRecoveryMultiplier    *int                 `json:"maxRecoveryMultiplier,omitempty"`
	DependsOn                []ExportedDependency `json:"dependsOn,omitempty"`
}

// MarshalExportDocument renders a canonical ExportDocument as pretty-printed
// (2-space) JSON in the wire format for its version. Version 2 documents go
// through the v2 wire projection (defaults block + duration strings); anything
// else falls back to the struct's own tags.
func MarshalExportDocument(doc *ExportDocument) ([]byte, error) {
	if doc.Version == ExportVersionV2 {
		wire, err := buildExportDocumentV2(doc)
		if err != nil {
			return nil, err
		}

		return json.MarshalIndent(wire, "", "  ")
	}

	return json.MarshalIndent(doc, "", "  ")
}

// buildExportDocumentV2 projects a canonical ExportDocument onto the v2 wire
// shape: it computes the modal default for every defaulted field and emits each
// check with only the fields that differ from the default.
//
//nolint:funlen // one pass over the checks to compute defaults + overrides
func buildExportDocumentV2(doc *ExportDocument) (*exportDocumentV2, error) {
	out := &exportDocumentV2{
		Version:      ExportVersionV2,
		ExportedAt:   doc.ExportedAt,
		Organization: doc.Organization,
		Secrets:      doc.Secrets,
		Checks:       make([]exportCheckV2, 0, len(doc.Checks)),
	}

	if len(doc.Checks) == 0 {
		return out, nil
	}

	// Collect each defaulted field across all checks (one pass), parsing the
	// period to seconds as we go so the modal math and the per-check override
	// rendering share the result.
	periodSecs := make([]int, len(doc.Checks))
	regionsList := make([][]string, len(doc.Checks))
	confirmationList := make([]int, len(doc.Checks))
	escalationList := make([]int, len(doc.Checks))
	recoveryList := make([]int, len(doc.Checks))
	flappingList := make([]int, len(doc.Checks))
	backoffList := make([]int, len(doc.Checks))
	maxRecoveryList := make([]int, len(doc.Checks))

	for i := range doc.Checks {
		check := &doc.Checks[i]

		secs, err := periodStringToSeconds(check.Period)
		if err != nil {
			return nil, fmt.Errorf("check %q period: %w", check.Slug, err)
		}

		periodSecs[i] = secs
		regionsList[i] = check.Regions
		confirmationList[i] = deref(check.ConfirmationPeriodSeconds)
		escalationList[i] = deref(check.EscalationThreshold)
		recoveryList[i] = deref(check.RecoveryPeriodSeconds)
		flappingList[i] = deref(check.FlappingWindowSeconds)
		backoffList[i] = deref(check.FlapBackoffFactor)
		maxRecoveryList[i] = deref(check.MaxRecoveryMultiplier)
	}

	// Modal (most frequent) default for every defaulted field.
	defRegions := modalRegions(regionsList)
	defPeriod := modalInt(periodSecs)
	defConfirmation := modalInt(confirmationList)
	defEscalation := modalInt(escalationList)
	defRecovery := modalInt(recoveryList)
	defFlappingWindow := modalInt(flappingList)
	defFlapBackoff := modalInt(backoffList)
	defMaxRecovery := modalInt(maxRecoveryList)

	out.Defaults = &exportDefaultsV2{
		Regions:               defRegions,
		Period:                formatDurationSecondsCompact(defPeriod),
		ConfirmationPeriod:    formatDurationSecondsCompact(defConfirmation),
		EscalationThreshold:   intPtr(defEscalation),
		RecoveryPeriod:        formatDurationSecondsCompact(defRecovery),
		FlappingWindow:        formatDurationSecondsCompact(defFlappingWindow),
		FlapBackoffFactor:     intPtr(defFlapBackoff),
		MaxRecoveryMultiplier: intPtr(defMaxRecovery),
	}

	for i := range doc.Checks {
		check := &doc.Checks[i]
		entry := exportCheckV2{
			Name:                     check.Name,
			Slug:                     check.Slug,
			PreviousSlug:             check.PreviousSlug,
			Description:              check.Description,
			Type:                     check.Type,
			Config:                   check.Config,
			Group:                    check.Group,
			Labels:                   check.Labels,
			Disabled:                 !check.Enabled,
			Internal:                 check.Internal,
			TracerouteOnFailure:      tracerouteWireValue(check.TracerouteOnFailure),
			ReopenCooldownMultiplier: check.ReopenCooldownMultiplier,
			DependsOn:                check.DependsOn,
		}

		if !equalStringSlice(check.Regions, defRegions) {
			entry.Regions = check.Regions
		}
		entry.Period = durationOverride(periodSecs[i], defPeriod)
		entry.ConfirmationPeriod = durationOverride(confirmationList[i], defConfirmation)
		entry.RecoveryPeriod = durationOverride(recoveryList[i], defRecovery)
		entry.FlappingWindow = durationOverride(flappingList[i], defFlappingWindow)
		entry.EscalationThreshold = intOverride(escalationList[i], defEscalation)
		entry.FlapBackoffFactor = intOverride(backoffList[i], defFlapBackoff)
		entry.MaxRecoveryMultiplier = intOverride(maxRecoveryList[i], defMaxRecovery)

		out.Checks = append(out.Checks, entry)
	}

	return out, nil
}

// UnmarshalJSON decodes both v1 and v2 export documents into the canonical
// ExportDocument. v1 decodes field-for-field; v2 decodes the wire shape and
// resolves every defaulted field (check value → document default → absent) so
// the rest of the import/apply pipeline is version-agnostic.
func (d *ExportDocument) UnmarshalJSON(data []byte) error {
	// Peek only the version so we can dispatch without a second full decode.
	var probe struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return fmt.Errorf("decode export version: %w", err)
	}

	if probe.Version == ExportVersionV2 {
		return d.unmarshalV2(data)
	}

	return d.unmarshalV1(data)
}

// unmarshalV1 decodes the legacy shape straight into the canonical struct via an
// alias (avoiding recursion), then maps the `_secretsStripped` meta flag onto
// the new Secrets marker.
func (d *ExportDocument) unmarshalV1(data []byte) error {
	type exportDocAlias ExportDocument
	var alias exportDocAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return fmt.Errorf("decode v1 export: %w", err)
	}
	*d = ExportDocument(alias)

	if d.Secrets == "" {
		var legacy struct {
			SecretsStripped bool `json:"_secretsStripped"` //nolint:tagliatelle // legacy v1 meta field name
		}
		if err := json.Unmarshal(data, &legacy); err == nil && legacy.SecretsStripped {
			d.Secrets = SecretsMarkerStripped
		}
	}

	return nil
}

// unmarshalV2 decodes the v2 wire shape and resolves per-check fields against
// the document defaults.
func (d *ExportDocument) unmarshalV2(data []byte) error {
	var wire exportDocumentV2
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("decode v2 export: %w", err)
	}

	d.Version = wire.Version
	d.ExportedAt = wire.ExportedAt
	d.Organization = wire.Organization
	d.Secrets = wire.Secrets
	d.Checks = make([]ExportCheck, len(wire.Checks))

	defaults := wire.Defaults
	if defaults == nil {
		defaults = &exportDefaultsV2{}
	}

	for i := range wire.Checks {
		resolved, err := resolveCheckV2(&wire.Checks[i], defaults)
		if err != nil {
			return err
		}
		d.Checks[i] = resolved
	}

	return nil
}

// resolveCheckV2 turns one v2 wire check into a canonical ExportCheck, applying
// the document defaults for any field the check omits. Duration strings are
// parsed to seconds; a nil result means "absent — use the system default".
func resolveCheckV2(wire *exportCheckV2, defaults *exportDefaultsV2) (ExportCheck, error) {
	out := ExportCheck{
		Name:                     wire.Name,
		Slug:                     wire.Slug,
		PreviousSlug:             wire.PreviousSlug,
		Description:              wire.Description,
		Type:                     wire.Type,
		Config:                   wire.Config,
		Labels:                   wire.Labels,
		Group:                    wire.Group,
		Enabled:                  !wire.Disabled,
		Internal:                 wire.Internal,
		TracerouteOnFailure:      wire.TracerouteOnFailure,
		ReopenCooldownMultiplier: wire.ReopenCooldownMultiplier,
		DependsOn:                wire.DependsOn,
	}

	// Regions: check value → document default → absent.
	switch {
	case len(wire.Regions) > 0:
		out.Regions = wire.Regions
	case len(defaults.Regions) > 0:
		out.Regions = defaults.Regions
	}

	// Period stays a string; the create/update path scans it (Go-style,
	// HH:MM:SS, or ISO8601 are all accepted).
	out.Period = firstNonEmpty(wire.Period, defaults.Period)

	confirmation, err := resolveDurationSeconds(wire.ConfirmationPeriod, defaults.ConfirmationPeriod)
	if err != nil {
		return ExportCheck{}, fmt.Errorf("check %q confirmationPeriod: %w", wire.Slug, err)
	}
	out.ConfirmationPeriodSeconds = confirmation

	recovery, err := resolveDurationSeconds(wire.RecoveryPeriod, defaults.RecoveryPeriod)
	if err != nil {
		return ExportCheck{}, fmt.Errorf("check %q recoveryPeriod: %w", wire.Slug, err)
	}
	out.RecoveryPeriodSeconds = recovery

	flapping, err := resolveDurationSeconds(wire.FlappingWindow, defaults.FlappingWindow)
	if err != nil {
		return ExportCheck{}, fmt.Errorf("check %q flappingWindow: %w", wire.Slug, err)
	}
	out.FlappingWindowSeconds = flapping

	out.EscalationThreshold = resolveIntPtr(wire.EscalationThreshold, defaults.EscalationThreshold)
	out.FlapBackoffFactor = resolveIntPtr(wire.FlapBackoffFactor, defaults.FlapBackoffFactor)
	out.MaxRecoveryMultiplier = resolveIntPtr(wire.MaxRecoveryMultiplier, defaults.MaxRecoveryMultiplier)

	return out, nil
}

// resolveDurationSeconds resolves a duration field: check value → document
// default → nil (absent). The winning string is parsed to whole seconds.
func resolveDurationSeconds(checkVal, defaultVal string) (*int, error) {
	chosen := firstNonEmpty(checkVal, defaultVal)
	if chosen == "" {
		return nil, nil //nolint:nilnil // absent field: no value, no error
	}

	secs, err := durationStringToSeconds(chosen)
	if err != nil {
		return nil, err
	}

	return &secs, nil
}

// resolveIntPtr resolves a unitless integer field: check value → document
// default → nil.
func resolveIntPtr(checkVal, defaultVal *int) *int {
	if checkVal != nil {
		v := *checkVal

		return &v
	}
	if defaultVal != nil {
		v := *defaultVal

		return &v
	}

	return nil
}

// durationStringToSeconds parses a duration string (Go-style "2m", the v1
// HH:MM:SS clock form, or ISO8601) into whole seconds.
func durationStringToSeconds(s string) (int, error) {
	var d timeutils.Duration
	if err := d.Scan(s); err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}

	return int(time.Duration(d) / time.Second), nil
}

// periodStringToSeconds parses a stored period string to whole seconds. An
// empty period resolves to 0 (the exporter never emits an empty period, but the
// modal math must not panic on one).
func periodStringToSeconds(s string) (int, error) {
	if s == "" {
		return 0, nil
	}

	return durationStringToSeconds(s)
}

// formatDurationSecondsCompact renders seconds as the shortest single-unit
// Go-style duration string: the largest unit (h, m, s) that divides evenly.
// 30 → "30s", 120 → "2m", 21600 → "6h", 90 → "90s", 0 → "0s".
func formatDurationSecondsCompact(seconds int) string {
	switch {
	case seconds == 0:
		return "0s"
	case seconds%3600 == 0:
		return strconv.Itoa(seconds/3600) + "h"
	case seconds%60 == 0:
		return strconv.Itoa(seconds/60) + "m"
	default:
		return strconv.Itoa(seconds) + "s"
	}
}

// durationOverride renders a per-check duration override, or "" when the value
// equals the document default (so the exporter omits it).
func durationOverride(actualSecs, defaultSecs int) string {
	if actualSecs == defaultSecs {
		return ""
	}

	return formatDurationSecondsCompact(actualSecs)
}

// intOverride returns a pointer to the per-check integer override, or nil when
// it equals the document default.
func intOverride(actual, def int) *int {
	if actual == def {
		return nil
	}

	return intPtr(actual)
}

// deref returns the pointed-to int, or 0 for a nil pointer. On export every
// alerting pointer is populated, so 0 only appears for a genuine zero value.
func deref(p *int) int {
	if p == nil {
		return 0
	}

	return *p
}

// firstNonEmpty returns a if it is non-empty, otherwise b.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}

	return b
}

// modalInt returns the most frequent value in values; ties break toward the
// smaller value for determinism. Returns 0 for an empty slice.
func modalInt(values []int) int {
	counts := make(map[int]int, len(values))
	for _, v := range values {
		counts[v]++
	}

	best, bestCount, seen := 0, 0, false
	for v, c := range counts {
		if !seen || c > bestCount || (c == bestCount && v < best) {
			best, bestCount, seen = v, c, true
		}
	}

	return best
}

// modalRegions returns the most frequent regions list; ties break toward the
// lexicographically smaller joined key for determinism.
func modalRegions(values [][]string) []string {
	type entry struct {
		value []string
		count int
	}

	byKey := make(map[string]*entry, len(values))
	for _, v := range values {
		key := regionsKey(v)
		if e, ok := byKey[key]; ok {
			e.count++
		} else {
			byKey[key] = &entry{value: v, count: 1}
		}
	}

	var bestKey string
	var best *entry
	for key, e := range byKey {
		if best == nil || e.count > best.count || (e.count == best.count && key < bestKey) {
			best, bestKey = e, key
		}
	}

	if best == nil {
		return nil
	}

	return best.value
}

// regionsKey builds a stable map key for a regions slice. Region slugs never
// contain a NUL byte, so it is an unambiguous separator.
func regionsKey(regions []string) string {
	out := make([]byte, 0, len(regions)*8)
	for i, r := range regions {
		if i > 0 {
			out = append(out, 0)
		}
		out = append(out, r...)
	}

	return string(out)
}

// equalStringSlice reports whether two string slices are element-wise equal.
func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// tracerouteWireValue omits the `inherit` state from a v2 document.
//
// `inherit` IS the absent state — a check that never set the policy and one
// explicitly set back to `inherit` are the same row — so writing it out would
// add a line to every check in every export to say nothing. `on` and `off` are
// the two that carry an operator's decision, and those always travel.
func tracerouteWireValue(policy string) string {
	if policy == TraceroutePolicyInherit {
		return ""
	}

	return policy
}
