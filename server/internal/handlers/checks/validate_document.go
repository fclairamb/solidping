package checks

import (
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
// document by ValidateDocument. Where is the check slug (or "document" for
// document-level problems); Message is human-readable.
type DocumentIssue struct {
	Where   string
	Message string
}

// labelKeyRegex matches a lowercase, kebab/dotted label key (mirrors the
// reference workflow's LABEL_KEY_RE).
var labelKeyRegex = regexp.MustCompile(`^[a-z0-9]+(?:[-.][a-z0-9]+)*$`)

// regionRegex matches a plain region slug or an "@org/private-location"
// reference (mirrors the reference workflow's REGION_RE).
var regionRegex = regexp.MustCompile(`^(?:[a-z0-9-]+|@[a-z0-9-]+/[a-z0-9-]+)$`)

// secretConfigHints are substrings that, found in a config key, suggest a
// credential was inlined instead of using a secret store / ${env:}/${param:}
// reference. Mirrors the reference workflow's SECRET_CONFIG_HINTS.
var secretConfigHints = []string{"user", "pass", "token", "secret", "auth", "credential", "apikey"}

// expectedStatusConfigKeys are the camelCase/snake_case spellings of the
// legacy and superseding HTTP status fields; a config must not set both.
var (
	expectedStatusKeys      = []string{"expectedStatus", "expected_status"}
	expectedStatusCodesKeys = []string{"expectedStatusCodes", "expected_status_codes"}
)

// ValidateDocument checks an already-parsed export/manifest document against
// the *generic* format rules shared by every SolidPing org: document shape,
// supported version, unique kebab-case slugs, per-type config keys (via each
// checker's own offline Validate), duration/label/region formats, no inlined
// credentials, expectedStatusCodes/expectedStatus exclusivity, and dependency
// graph soundness (parents exist, no self-edges, no cycles). It performs no
// I/O — safe to run with no token and no network. Org-specific convention
// rules (stack topology, RabbitMQ per-env symmetry, etc.) are out of scope by
// design; they belong to the workflow that owns those conventions, not to the
// document format.
//
//nolint:cyclop,funlen,gocognit // one pass over every generic document rule
func ValidateDocument(doc *ExportDocument) []DocumentIssue {
	var issues []DocumentIssue

	if !isSupportedExportVersion(doc.Version) {
		issues = append(issues, DocumentIssue{
			Where: "document", Message: fmt.Sprintf("version must be 1 or 2, got %d", doc.Version),
		})
	}

	if doc.Organization == "" {
		issues = append(issues, DocumentIssue{Where: "document", Message: "organization is missing"})
	}

	if doc.Secrets != "" && doc.Secrets != SecretsMarkerStripped {
		issues = append(issues, DocumentIssue{
			Where: "document",
			Message: fmt.Sprintf(
				"secrets must stay %q, got %q — never commit a raw export that still carries credentials",
				SecretsMarkerStripped, doc.Secrets),
		})
	}

	if len(doc.Checks) == 0 {
		issues = append(issues, DocumentIssue{Where: "document", Message: "checks must be a non-empty list"})

		return issues
	}

	seenSlugs := make(map[string]struct{}, len(doc.Checks))
	knownSlugs := make(map[string]struct{}, len(doc.Checks))

	for i := range doc.Checks {
		check := &doc.Checks[i]
		if check.Slug != "" {
			knownSlugs[check.Slug] = struct{}{}
		}
	}

	for i := range doc.Checks {
		check := &doc.Checks[i]
		where := check.Slug
		if where == "" {
			where = fmt.Sprintf("<check %d>", i)
		}

		if check.Name == "" {
			issues = append(issues, DocumentIssue{Where: where, Message: "missing required field \"name\""})
		}

		if check.Slug == "" {
			issues = append(issues, DocumentIssue{Where: where, Message: "missing required field \"slug\""})
		} else if err := validateSlug(check.Slug); err != nil {
			issues = append(issues, DocumentIssue{Where: where, Message: err.Error()})
		}

		if check.Slug != "" {
			if _, dup := seenSlugs[check.Slug]; dup {
				issues = append(issues, DocumentIssue{Where: where, Message: "duplicate slug"})
			}
			seenSlugs[check.Slug] = struct{}{}
		}

		issues = append(issues, validateCheckType(where, check)...)
		issues = append(issues, validateCheckFormats(where, check)...)
	}

	issues = append(issues, validateDependencyGraph(doc.Checks, knownSlugs)...)

	return issues
}

// validateCheckType validates the check's type and, via the registered
// checker's own offline Validate, its per-type config keys — reusing the
// exact code path the live create/update/live-validate handlers use.
func validateCheckType(where string, check *ExportCheck) []DocumentIssue {
	var issues []DocumentIssue

	if check.Type == "" {
		issues = append(issues, DocumentIssue{Where: where, Message: "missing required field \"type\""})

		return issues
	}

	checker, ok := registry.GetChecker(checkerdef.CheckType(check.Type))
	if !ok {
		issues = append(issues, DocumentIssue{Where: where, Message: fmt.Sprintf("unsupported check type %q", check.Type)})

		return issues
	}

	if check.Config == nil {
		issues = append(issues, DocumentIssue{
			Where: where, Message: "config is missing or null — use \"config: {}\" when there is nothing to set",
		})

		return issues
	}

	if err := checker.Validate(&checkerdef.CheckSpec{Config: check.Config}); err != nil {
		issues = append(issues, DocumentIssue{Where: where, Message: err.Error()})
	}

	issues = append(issues, validateNoInlinedCredentials(where, check.Config)...)
	issues = append(issues, validateStatusFieldExclusivity(where, check.Config)...)

	return issues
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
		for _, hint := range secretConfigHints {
			if strings.Contains(lower, hint) {
				issues = append(issues, DocumentIssue{
					Where: where,
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

	if hasAny(expectedStatusKeys) && hasAny(expectedStatusCodesKeys) {
		return []DocumentIssue{{
			Where: where,
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
				Where: where, Message: fmt.Sprintf("period %q is not a duration like \"30s\", \"15m\" or \"12h\"", check.Period),
			})
		}
	}

	for key, value := range check.Labels {
		if !labelKeyRegex.MatchString(key) {
			issues = append(issues, DocumentIssue{Where: where, Message: fmt.Sprintf("label key %q must be lowercase kebab/dotted", key)})
		}
		if value == "" {
			issues = append(issues, DocumentIssue{Where: where, Message: fmt.Sprintf("label %q must have a non-empty string value", key)})
		}
	}

	for _, region := range check.Regions {
		if !regionRegex.MatchString(region) {
			issues = append(issues, DocumentIssue{
				Where: where, Message: fmt.Sprintf("region %q must be a slug or \"@org/private-location\"", region),
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

		for _, dep := range check.DependsOn {
			if !models.CheckDependencyKind(dep.Kind).IsValid() {
				issues = append(issues, DocumentIssue{
					Where: where, Message: fmt.Sprintf("dependsOn %q has kind %q, expected \"hard\" or \"soft\"", dep.ParentSlug, dep.Kind),
				})
			}

			switch {
			case dep.ParentSlug == "":
				issues = append(issues, DocumentIssue{Where: where, Message: "dependsOn entry is missing parentSlug"})
			case dep.ParentSlug == check.Slug:
				issues = append(issues, DocumentIssue{Where: where, Message: "check depends on itself"})
			default:
				if _, ok := knownSlugs[dep.ParentSlug]; !ok {
					issues = append(issues, DocumentIssue{
						Where: where, Message: fmt.Sprintf("dependsOn parentSlug %q does not match any check", dep.ParentSlug),
					})

					continue
				}
				if _, dup := seenParents[dep.ParentSlug]; dup {
					issues = append(issues, DocumentIssue{Where: where, Message: fmt.Sprintf("dependsOn lists %q twice", dep.ParentSlug)})

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

// dfs colour marks for cycle detection.
const (
	colourWhite = 0
	colourGrey  = 1
	colourBlack = 2
)

// findDependencyCycles runs a DFS over the parent edges, reporting each cycle
// once (mirrors the reference workflow's report_cycles).
func findDependencyCycles(edges map[string][]string) []DocumentIssue {
	var issues []DocumentIssue

	colour := make(map[string]int, len(edges))
	seenCycles := make(map[string]struct{})

	nodes := make([]string, 0, len(edges))
	for node := range edges {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)

	var walk func(node string, path []string)
	walk = func(node string, path []string) {
		colour[node] = colourGrey
		path = append(path, node)

		for _, parent := range edges[node] {
			switch colour[parent] {
			case colourGrey:
				idx := indexOf(path, parent)
				cycle := append(append([]string{}, path[idx:]...), parent)
				key := cycleKey(cycle)
				if _, ok := seenCycles[key]; !ok {
					seenCycles[key] = struct{}{}
					issues = append(issues, DocumentIssue{
						Where: node, Message: "dependency cycle: " + strings.Join(cycle, " -> "),
					})
				}
			case colourWhite:
				walk(parent, path)
			}
		}

		colour[node] = colourBlack
	}

	for _, node := range nodes {
		if colour[node] == colourWhite {
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
