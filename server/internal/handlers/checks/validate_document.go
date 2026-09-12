package checks

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// DocumentIssue is one generic-format problem found in an export/manifest
// document by ValidateDocument. Where is the check slug (or docWhere for
// document-level problems); Field names the offending property; Code is the
// STABLE machine code a CI job may allow-list; Message is human-readable prose
// and may be reworded at any time.
//
// The JSON spelling is `{slug, field, code, message}` — the shape spec
// 2026-09-11-04 pins for POST /checks/validate on a whole document. `Where` is
// kept as the Go field name because it is `document` for document-level
// problems, which is not a slug.
type DocumentIssue struct {
	Where   string `json:"slug"`
	Field   string `json:"field,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Stable machine codes for DocumentIssue. A CI job branches on these; the
// prose in Message is not part of the contract. Every code this package can
// emit is listed here, and DocumentIssueCodes() returns the closed set so the
// documentation and the tests read it from the code rather than restating it.
const (
	// CodeUnsupportedVersion is a document `version` this build cannot read.
	CodeUnsupportedVersion = "UNSUPPORTED_VERSION"
	// CodeMissingOrganization is an absent document `organization`.
	CodeMissingOrganization = "MISSING_ORGANIZATION"
	// CodeInvalidSecretsMarker is a `secrets` marker other than "stripped".
	CodeInvalidSecretsMarker = "INVALID_SECRETS_MARKER"
	// CodeEmptyChecks is a document whose `checks` list is empty or absent.
	CodeEmptyChecks = "EMPTY_CHECKS"
	// CodeMissingField is a required per-check field (name, slug, type,
	// config) that the document does not carry.
	CodeMissingField = "MISSING_FIELD"
	// CodeDuplicateSlug is a slug used by more than one check in the document.
	CodeDuplicateSlug = "DUPLICATE_SLUG"
	// CodeUnknownType is a check type no checker in this build implements.
	// Distinct from CodeUnsupportedType (the single-check endpoint's code) so
	// the two surfaces can be told apart in a log.
	CodeUnknownType = "UNKNOWN_TYPE"
	// CodeInlinedCredential flags a config key whose NAME suggests a literal
	// credential. A hint, not a proof: `username` and ftp's `passive_mode`
	// both trip it.
	CodeInlinedCredential = "INLINED_CREDENTIAL"
	// CodeStatusFieldConflict is expectedStatus and expectedStatusCodes set on
	// the same config.
	CodeStatusFieldConflict = "STATUS_FIELD_CONFLICT"
	// CodeInvalidLabel is a label key or value the database would refuse.
	CodeInvalidLabel = "INVALID_LABEL"
	// CodeRegionFormat is a region that is neither a slug nor "@location".
	CodeRegionFormat = "REGION_FORMAT"
	// CodeDependencyCycle is a cycle in the dependsOn graph.
	CodeDependencyCycle = "DEPENDENCY_CYCLE"
	// CodeUnresolvedSecretRef is a ${env:}/${param:} reference that does not
	// resolve for this organization. Only the ORG-AWARE endpoint emits it:
	// ValidateDocument performs no I/O and cannot know.
	CodeUnresolvedSecretRef = "UNRESOLVED_SECRET_REF"
)

// DocumentIssueCodes returns every code a document validation can report, in a
// stable order. It is what the API documentation and the CLI's --help print,
// so the published allow-list can never drift from the emitted one.
func DocumentIssueCodes() []string {
	return []string{
		CodeUnsupportedVersion,
		CodeMissingOrganization,
		CodeInvalidSecretsMarker,
		CodeEmptyChecks,
		CodeMissingField,
		CodeInvalidSlug,
		CodeDuplicateSlug,
		CodeInternalNotWritable,
		CodeUnknownType,
		CodeInvalidConfig,
		CodeInlinedCredential,
		CodeStatusFieldConflict,
		CodeInvalidPeriod,
		CodeInvalidLabel,
		CodeRegionFormat,
		CodeInvalidDependsOn,
		CodeDependencyCycle,
		CodeUnresolvedSecretRef,
	}
}

// Field names used by DocumentIssue.Field, mirroring the document's own JSON
// property names.
const (
	fieldDocVersion      = "version"
	fieldDocOrganization = "organization"
	fieldDocSecrets      = "secrets"
	fieldDocChecks       = "checks"
	fieldConfig          = "config"
	fieldRegions         = "regions"
	fieldDependsOn       = "dependsOn"
	fieldLabelsPrefix    = "labels."
	fieldConfigPrefix    = "config."
)

// docWhere is the DocumentIssue.Where value used for document-level (not
// per-check) problems.
const docWhere = "document"

// issueDuplicateSlug is the message used for a repeated slug — factored out
// since it's asserted on by name in tests and would otherwise appear
// literally three times.
const issueDuplicateSlug = "duplicate slug"

// regionRegex matches a plain cloud region slug, an org-relative private region
// ("@private-location", the stored form since spec 2026-08-13-01), or the LEGACY
// fully-qualified "@org/private-location" spelling — which stays accepted here
// because regions.NormalizeRegionsForOrg is what folds it down (for this org) or
// rejects it (for anybody else's) on the way in.
var regionRegex = regexp.MustCompile(`^(?:[a-z0-9-]+|@[a-z0-9-]+(?:/[a-z0-9-]+)?)$`)

// secretConfigHints are substrings that, found in a config key, suggest a
// credential was inlined instead of using a secret store / ${env:}/${param:}
// reference. Mirrors the reference workflow's SECRET_CONFIG_HINTS.
func secretConfigHints() []string {
	return []string{"user", "pass", "token", "secret", "auth", "credential", "apikey"}
}

// expectedStatusFieldKeys are the camelCase/snake_case spellings of the
// legacy expectedStatus field and its superseding expectedStatusCodes field;
// a config must not set both.
func expectedStatusFieldKeys() ([]string, []string) {
	return []string{"expectedStatus", "expected_status"}, []string{"expectedStatusCodes", "expected_status_codes"}
}

// ValidateDocument checks an already-parsed export/manifest document against
// the *generic* format rules shared by every SolidPing org: document shape,
// supported version, unique kebab-case slugs, per-type config keys (via each
// checker's own offline Validate), duration/label/region formats, no inlined
// credentials, expectedStatusCodes/expectedStatus exclusivity, and dependency
// graph soundness (parents exist, no self-edges, no cycles). It performs no
// I/O — safe to run with no token and no network — and never mutates doc:
// each check's Config is deep-copied before being handed to a checker's
// Validate, since some checkers (heartbeat, email) mutate their input config
// in place to auto-generate a token when one is absent. Org-specific convention
// rules (stack topology, RabbitMQ per-env symmetry, etc.) are out of scope by
// design; they belong to the workflow that owns those conventions, not to the
// document format.
func ValidateDocument(doc *ExportDocument) []DocumentIssue {
	issues := validateDocumentShape(doc)
	if len(doc.Checks) == 0 {
		return issues
	}

	knownSlugs := make(map[string]struct{}, len(doc.Checks))
	for i := range doc.Checks {
		if doc.Checks[i].Slug != "" {
			knownSlugs[doc.Checks[i].Slug] = struct{}{}
		}
	}

	seenSlugs := make(map[string]struct{}, len(doc.Checks))
	for i := range doc.Checks {
		issues = append(issues, validateSingleCheck(&doc.Checks[i], i, seenSlugs)...)
	}

	issues = append(issues, validateDependencyGraph(doc.Checks, knownSlugs)...)

	return issues
}

// validateDocumentShape validates the document-level fields: version,
// organization, secrets marker, and a non-empty checks list.
func validateDocumentShape(doc *ExportDocument) []DocumentIssue {
	var issues []DocumentIssue

	if !isSupportedExportVersion(doc.Version) {
		issues = append(issues, DocumentIssue{
			Where: docWhere, Field: fieldDocVersion, Code: CodeUnsupportedVersion,
			Message: fmt.Sprintf("version must be 1 or 2, got %d", doc.Version),
		})
	}

	if doc.Organization == "" {
		issues = append(issues, DocumentIssue{
			Where: docWhere, Field: fieldDocOrganization, Code: CodeMissingOrganization,
			Message: "organization is missing",
		})
	}

	if doc.Secrets != "" && doc.Secrets != SecretsMarkerStripped {
		issues = append(issues, DocumentIssue{
			Where: docWhere, Field: fieldDocSecrets, Code: CodeInvalidSecretsMarker,
			Message: fmt.Sprintf(
				"secrets must stay %q, got %q — never commit a raw export that still carries credentials",
				SecretsMarkerStripped, doc.Secrets),
		})
	}

	if len(doc.Checks) == 0 {
		issues = append(issues, DocumentIssue{
			Where: docWhere, Field: fieldDocChecks, Code: CodeEmptyChecks,
			Message: "checks must be a non-empty list",
		})
	}

	return issues
}

// validateSingleCheck validates one check's own fields (name, slug,
// uniqueness, type, config, formats) — everything except the dependency
// graph, which needs the whole document at once.
func validateSingleCheck(check *ExportCheck, index int, seenSlugs map[string]struct{}) []DocumentIssue {
	var issues []DocumentIssue

	where := check.Slug
	if where == "" {
		where = fmt.Sprintf("<check %d>", index)
	}

	if check.Name == "" {
		issues = append(issues, DocumentIssue{
			Where: where, Field: fieldName, Code: CodeMissingField,
			Message: "missing required field \"name\"",
		})
	}

	if check.Slug == "" {
		issues = append(issues, DocumentIssue{
			Where: where, Field: fieldSlug, Code: CodeMissingField,
			Message: "missing required field \"slug\"",
		})
	} else if err := validateSlug(check.Slug); err != nil {
		issues = append(issues, DocumentIssue{
			Where: where, Field: fieldSlug, Code: CodeInvalidSlug, Message: err.Error(),
		})
	}

	if check.Slug != "" {
		if _, dup := seenSlugs[check.Slug]; dup {
			issues = append(issues, DocumentIssue{
				Where: where, Field: fieldSlug, Code: CodeDuplicateSlug, Message: issueDuplicateSlug,
			})
		}
		seenSlugs[check.Slug] = struct{}{}
	}

	// `internal` is server-owned and refused on import (spec 2026-08-27-01).
	// Reported here too so `validate` and `apply` agree about what a document
	// may contain — a validator that green-lights a manifest the applier will
	// reject is worse than no validator.
	if check.Internal {
		issues = append(issues, DocumentIssue{
			Where: where, Field: fieldInternal, Code: CodeInternalNotWritable,
			Message: "internal: " + ErrInternalFieldNotWritable.Error(),
		})
	}

	issues = append(issues, validateCheckType(where, check)...)
	issues = append(issues, validateCheckFormats(where, check)...)

	return issues
}

// validateCheckType validates the check's type and, via the registered
// checker's own offline Validate, its per-type config keys — reusing the
// exact code path the live create/update/live-validate handlers use.
func validateCheckType(where string, check *ExportCheck) []DocumentIssue {
	var issues []DocumentIssue

	if check.Type == "" {
		issues = append(issues, DocumentIssue{
			Where: where, Field: fieldType, Code: CodeMissingField,
			Message: "missing required field \"type\"",
		})

		return issues
	}

	checker, ok := registry.GetChecker(checkerdef.CheckType(check.Type))
	if !ok {
		issues = append(issues, DocumentIssue{
			Where: where, Field: fieldType, Code: CodeUnknownType,
			Message: fmt.Sprintf("unsupported check type %q", check.Type),
		})

		return issues
	}

	if check.Config == nil {
		issues = append(issues, DocumentIssue{
			Where: where, Field: fieldConfig, Code: CodeMissingField,
			Message: "config is missing or null — use \"config: {}\" when there is nothing to set",
		})

		return issues
	}

	// checker.Validate is documented as read-only ("shall not perform any
	// network operations") but at least two checkers (heartbeat, email)
	// mutate spec.Config in place to auto-generate a token when one is
	// absent — correct for the live create/update path, wrong for an
	// offline validator that must never change the document it's checking.
	// Pass a deep copy so ValidateDocument stays pure regardless of what an
	// individual checker's Validate does to the map it's handed.
	configCopy, copyErr := deepCopyConfig(check.Config)
	if copyErr != nil {
		issues = append(issues, DocumentIssue{
			Where: where, Field: fieldConfig, Code: CodeInvalidConfig,
			Message: fmt.Sprintf("config is not representable as JSON: %v", copyErr),
		})

		return issues
	}

	if err := checker.Validate(&checkerdef.CheckSpec{Config: configCopy}); err != nil {
		issues = append(issues, configIssue(where, err))
	}

	// Shared, type-agnostic config keys the per-type Validate never sees.
	if err := validateIPVersionConfig(check.Type, check.Config); err != nil {
		issues = append(issues, configIssue(where, err))
	}

	// Credential/status-field checks run on the caller's original config —
	// never on configCopy, which a checker may have mutated.
	issues = append(issues, validateNoInlinedCredentials(where, check.Config)...)
	issues = append(issues, validateStatusFieldExclusivity(where, check.Config)...)

	return issues
}

// configIssue renders a config-level validator error as an issue, preferring
// the exact parameter a *ConfigError names over the generic "config" field —
// the same precedence validateFindings.addErrorFrom uses on the single-check
// endpoint, so the two surfaces point at the same property.
func configIssue(where string, err error) DocumentIssue {
	field := fieldConfig
	if configErr := checkerdef.IsConfigError(err); configErr != nil && configErr.Parameter != "" {
		field = fieldConfigPrefix + configErr.Parameter
	}

	return DocumentIssue{Where: where, Field: field, Code: CodeInvalidConfig, Message: err.Error()}
}

// deepCopyConfig returns an independent copy of a check config map so it can
// be handed to a checker's Validate without risking a mutation leaking back
// into the caller's document. Config values always originate from decoding
// JSON (an export/manifest document), so a JSON marshal/unmarshal round trip
// is a correct and simple deep copy.
func deepCopyConfig(config map[string]any) (map[string]any, error) {
	if config == nil {
		return nil, nil //nolint:nilnil // nil config is a valid "no copy needed" case, not an error
	}

	raw, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}

	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	return out, nil
}

// validateNoInlinedCredentials flags config keys that look like a literal
// credential rather than a ${env:}/${param:} reference or SolidPing's own
// secret store.
func validateNoInlinedCredentials(where string, config map[string]any) []DocumentIssue {
	var issues []DocumentIssue

	keys := make([]string, 0, len(config))
	for k := range config {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		lower := strings.ToLower(key)
		for _, hint := range secretConfigHints() {
			if strings.Contains(lower, hint) {
				issues = append(issues, DocumentIssue{
					Where: where, Field: fieldConfigPrefix + key, Code: CodeInlinedCredential,
					Message: fmt.Sprintf(
						"config.%s looks like a credential — keep it in SolidPing's own secret store, not in this file",
						key),
				})

				break
			}
		}
	}

	return issues
}

// validateStatusFieldExclusivity enforces that expectedStatusCodes supersedes
// the legacy expectedStatus — never both on the same config.
func validateStatusFieldExclusivity(where string, config map[string]any) []DocumentIssue {
	hasAny := func(keys []string) bool {
		for _, k := range keys {
			if _, ok := config[k]; ok {
				return true
			}
		}

		return false
	}

	statusKeys, statusCodesKeys := expectedStatusFieldKeys()
	if hasAny(statusKeys) && hasAny(statusCodesKeys) {
		return []DocumentIssue{{
			Where: where, Field: "config.expectedStatusCodes", Code: CodeStatusFieldConflict,
			Message: "config sets both expectedStatus and expectedStatusCodes — the latter supersedes " +
				"the former, so drop expectedStatus rather than leaving it as dead config",
		}}
	}

	return nil
}

// validateCheckFormats validates the duration, label, and region formats on a
// check — fields common to every check type, so they live outside the
// per-type checker.Validate path.
func validateCheckFormats(where string, check *ExportCheck) []DocumentIssue {
	var issues []DocumentIssue

	if check.Period != "" {
		var d timeutils.Duration
		if err := d.Scan(check.Period); err != nil {
			issues = append(issues, DocumentIssue{
				Where: where, Field: fieldPeriod, Code: CodeInvalidPeriod,
				Message: fmt.Sprintf("period %q is not a duration like \"30s\", \"15m\" or \"12h\"", check.Period),
			})
		}
	}

	// The label rules are the canonical ones (models.ValidateLabelKey /
	// ValidateLabelValue) — the same code the create/update/import write paths
	// run. This validator used to carry its own, laxer regex that accepted
	// 1-2 char keys, leading digits and dots: it green-lit documents Postgres
	// could not store (spec 2026-09-10-01). Keys are walked in sorted order so
	// a document always produces its issues in the same order.
	labelKeys := make([]string, 0, len(check.Labels))
	for key := range check.Labels {
		labelKeys = append(labelKeys, key)
	}
	sort.Strings(labelKeys)

	for _, key := range labelKeys {
		if err := models.ValidateLabelKey(key); err != nil {
			issues = append(issues, DocumentIssue{
				Where: where, Field: fieldLabelsPrefix + key, Code: CodeInvalidLabel, Message: err.Error(),
			})
		}
		if err := models.ValidateLabelValue(key, check.Labels[key]); err != nil {
			issues = append(issues, DocumentIssue{
				Where: where, Field: fieldLabelsPrefix + key, Code: CodeInvalidLabel, Message: err.Error(),
			})
		}
	}

	for _, region := range check.Regions {
		if !regionRegex.MatchString(region) {
			issues = append(issues, DocumentIssue{
				Where: where, Field: fieldRegions, Code: CodeRegionFormat,
				Message: fmt.Sprintf("region %q must be a slug or \"@private-location\"", region),
			})
		}
	}

	return issues
}

// validateDependencyGraph validates the dependsOn edges across the whole
// document: parents must exist within the document, no self-edges, no
// duplicate parent on the same check, a known kind, and no cycles.
func validateDependencyGraph(checks []ExportCheck, knownSlugs map[string]struct{}) []DocumentIssue {
	var issues []DocumentIssue

	edges := make(map[string][]string, len(checks))

	for i := range checks {
		check := &checks[i]
		where := check.Slug
		if where == "" {
			continue
		}

		seenParents := make(map[string]struct{}, len(check.DependsOn))
		parents := make([]string, 0, len(check.DependsOn))

		for depIdx := range check.DependsOn {
			dep := &check.DependsOn[depIdx]
			if !models.CheckDependencyKind(dep.Kind).IsValid() {
				issues = append(issues, DocumentIssue{
					Where: where, Field: fieldDependsOn, Code: CodeInvalidDependsOn,
					Message: fmt.Sprintf(
						"dependsOn %q has kind %q, expected \"hard\" or \"soft\"", dep.ParentSlug, dep.Kind),
				})
			}

			switch dep.ParentSlug {
			case "":
				issues = append(issues, DocumentIssue{
					Where: where, Field: fieldDependsOn, Code: CodeInvalidDependsOn,
					Message: "dependsOn entry is missing parentSlug",
				})
			case check.Slug:
				issues = append(issues, DocumentIssue{
					Where: where, Field: fieldDependsOn, Code: CodeInvalidDependsOn,
					Message: "check depends on itself",
				})
			default:
				if _, ok := knownSlugs[dep.ParentSlug]; !ok {
					issues = append(issues, DocumentIssue{
						Where: where, Field: fieldDependsOn, Code: CodeInvalidDependsOn,
						Message: fmt.Sprintf("dependsOn parentSlug %q does not match any check", dep.ParentSlug),
					})

					continue
				}
				if _, dup := seenParents[dep.ParentSlug]; dup {
					issues = append(issues, DocumentIssue{
						Where: where, Field: fieldDependsOn, Code: CodeInvalidDependsOn,
						Message: fmt.Sprintf("dependsOn lists %q twice", dep.ParentSlug),
					})

					continue
				}
				seenParents[dep.ParentSlug] = struct{}{}
				parents = append(parents, dep.ParentSlug)
			}
		}

		edges[check.Slug] = parents
	}

	issues = append(issues, findDependencyCycles(edges)...)

	return issues
}

// dfs color marks for cycle detection.
const (
	colorWhite = 0
	colorGrey  = 1
	colorBlack = 2
)

// findDependencyCycles runs a DFS over the parent edges, reporting each cycle
// once (mirrors the reference workflow's report_cycles).
func findDependencyCycles(edges map[string][]string) []DocumentIssue {
	var issues []DocumentIssue

	color := make(map[string]int, len(edges))
	seenCycles := make(map[string]struct{})

	nodes := make([]string, 0, len(edges))
	for node := range edges {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)

	var walk func(node string, path []string)
	walk = func(node string, path []string) {
		color[node] = colorGrey
		path = append(path, node)

		for _, parent := range edges[node] {
			switch color[parent] {
			case colorGrey:
				idx := indexOf(path, parent)
				cycle := append(append([]string{}, path[idx:]...), parent)
				key := cycleKey(cycle)
				if _, ok := seenCycles[key]; !ok {
					seenCycles[key] = struct{}{}
					issues = append(issues, DocumentIssue{
						Where: node, Field: fieldDependsOn, Code: CodeDependencyCycle,
						Message: "dependency cycle: " + strings.Join(cycle, " -> "),
					})
				}
			case colorWhite:
				walk(parent, path)
			}
		}

		color[node] = colorBlack
	}

	for _, node := range nodes {
		if color[node] == colorWhite {
			walk(node, nil)
		}
	}

	return issues
}

func indexOf(s []string, v string) int {
	for i, e := range s {
		if e == v {
			return i
		}
	}

	return 0
}

// cycleKey builds a stable dedup key for a cycle regardless of which node the
// DFS happened to detect it from.
func cycleKey(cycle []string) string {
	unique := make([]string, 0, len(cycle))
	seen := make(map[string]struct{}, len(cycle))
	for _, n := range cycle {
		if _, ok := seen[n]; !ok {
			seen[n] = struct{}{}
			unique = append(unique, n)
		}
	}
	sort.Strings(unique)

	return strings.Join(unique, ",")
}
