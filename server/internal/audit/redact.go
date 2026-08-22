package audit

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// maxValueLen caps any single string that reaches the trail. An audit row is
// evidence, not a copy of the request body: a 40 KB webhook template in a
// payload is both useless to a reader and a good way to smuggle a secret past
// a key-name filter.
const maxValueLen = 256

// maxDepth bounds nesting. Anything deeper is dropped wholesale rather than
// walked — a deeply nested blob is a config payload, which the spec says must
// never be stored.
const maxDepth = 3

// sensitiveKeyFragments are matched case-insensitively as SUBSTRINGS of a
// payload key. Substring rather than exact match on purpose: the failure mode
// to avoid is a new field called `slack_bot_token` or `smtp_password_2`
// slipping through a list that only knew about `token` and `password`.
//
// The cost of over-matching is a dropped field in an audit row; the cost of
// under-matching is a credential in a table an org admin can read. Those are
// not symmetric, so this list errs long.
//
//nolint:gochecknoglobals // static denylist, treated as a constant.
var sensitiveKeyFragments = []string{
	"apikey",
	"auth",
	"cert",
	"credential",
	"cookie",
	"dsn",
	"hash",
	"hook_url",
	"key",
	"nonce",
	"passphrase",
	"passwd",
	"password",
	"private",
	"salt",
	"secret",
	"session",
	"signature",
	"token",
	"webhook",
}

// safeKeys survive the denylist verbatim. Each is a field the audit trail
// genuinely needs whose NAME happens to contain a sensitive fragment — the
// token identity fields the spec explicitly asks for ("token events store the
// token's name and prefix, never the value"), and the boolean/enumerated
// facts that say what KIND of credential was involved without revealing any
// part of it.
//
//nolint:gochecknoglobals // static allowlist, treated as a constant.
var safeKeys = map[string]bool{
	"token_kind":     true,
	"token_name":     true,
	"token_prefix":   true,
	"token_uid":      true,
	"auth_method":    true,
	"key_name":       true,
	"key_uid":        true,
	"webhook_count":  true,
	"has_credential": true,
}

// IsSensitiveKey reports whether a payload key must never carry a value into
// the audit trail. Exported so emitters can name a field in changed_fields
// while deliberately omitting it from changes, and so tests can assert the
// classification directly.
func IsSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	if safeKeys[lower] {
		return false
	}

	for _, fragment := range sensitiveKeyFragments {
		if strings.Contains(lower, fragment) {
			return true
		}
	}

	return false
}

// Redact returns a copy of payload with every sensitive key removed, every
// string truncated, and anything nested deeper than maxDepth dropped. It never
// mutates its argument and never returns nil for a non-nil input.
func Redact(payload models.JSONMap) models.JSONMap {
	if payload == nil {
		return nil
	}

	out := make(models.JSONMap, len(payload))

	for key, value := range payload {
		if IsSensitiveKey(key) {
			continue
		}

		if clean, ok := redactValue(value, 1); ok {
			out[key] = clean
		}
	}

	return out
}

func redactValue(value any, depth int) (any, bool) {
	if depth > maxDepth {
		return nil, false
	}

	switch typed := value.(type) {
	case string:
		return truncate(typed, maxValueLen), true
	case map[string]any:
		return redactMap(typed, depth), true
	case models.JSONMap:
		return redactMap(map[string]any(typed), depth), true
	case []any:
		return redactSlice(typed, depth), true
	case []string:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, truncate(item, maxValueLen))
		}

		return out, true
	default:
		return value, true
	}
}

func redactMap(in map[string]any, depth int) map[string]any {
	out := make(map[string]any, len(in))

	for key, value := range in {
		if IsSensitiveKey(key) {
			continue
		}

		if clean, ok := redactValue(value, depth+1); ok {
			out[key] = clean
		}
	}

	return out
}

func redactSlice(in []any, depth int) []any {
	out := make([]any, 0, len(in))

	for _, item := range in {
		if clean, ok := redactValue(item, depth+1); ok {
			out = append(out, clean)
		}
	}

	return out
}

// Changes compares a before/after snapshot and returns
//
//   - changed: the sorted names of every field that moved, INCLUDING sensitive
//     ones. Knowing that the webhook secret was rotated is exactly the kind of
//     fact an audit trail exists for; knowing its value is not.
//   - safe: old→new pairs for the non-sensitive subset whose values are
//     scalars short enough to be evidence rather than a payload copy.
//
// Both halves land in the event as changed_fields / changes.
func Changes(before, after map[string]any) ([]string, map[string]any) {
	keys := make(map[string]bool, len(before)+len(after))
	for key := range before {
		keys[key] = true
	}

	for key := range after {
		keys[key] = true
	}

	changed := make([]string, 0, len(keys))
	safe := make(map[string]any)

	for key := range keys {
		oldValue, oldOK := before[key]
		newValue, newOK := after[key]

		if oldOK == newOK && scalarEqual(oldValue, newValue) {
			continue
		}

		changed = append(changed, key)

		if IsSensitiveKey(key) {
			continue
		}

		oldSafe, oldScalar := safeScalar(oldValue)
		newSafe, newScalar := safeScalar(newValue)

		if oldScalar && newScalar {
			safe[key] = map[string]any{"from": oldSafe, "to": newSafe}
		}
	}

	sort.Strings(changed)

	if len(safe) == 0 {
		safe = nil
	}

	return changed, safe
}

// scalarEqual compares two values by their canonical string form. That is
// deliberately coarse: it treats int64(3) and float64(3) as equal, which is
// what a JSON round-trip does to a value anyway, so a snapshot that has been
// through the database does not manufacture phantom changes.
func scalarEqual(a, b any) bool {
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

// safeScalar reports whether a value is a short scalar that may be quoted
// verbatim in the trail. Structs, maps and slices never qualify — that is the
// rule keeping "full config payloads" out.
func safeScalar(value any) (any, bool) {
	switch typed := value.(type) {
	case nil:
		return nil, true
	case string:
		if len(typed) > maxValueLen {
			return nil, false
		}

		return typed, true
	case bool, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, float32, float64:
		return typed, true
	default:
		return nil, false
	}
}

// ChangePayload assembles the canonical update-event payload: the sorted
// changed field names, the safe old→new subset, plus any extra facts the
// emitter wants to add. extraFields names changes that are not expressible as
// a scalar diff (e.g. "steps", "checks") and are folded into changed_fields.
//
// Centralized so every *.updated event in the product has the same shape and
// dash0 needs exactly one renderer.
func ChangePayload(
	changed []string, safe map[string]any, extraFields []string, extra models.JSONMap,
) models.JSONMap {
	all := make([]string, 0, len(changed)+len(extraFields))
	seen := make(map[string]bool, len(changed)+len(extraFields))

	for _, field := range append(append([]string{}, changed...), extraFields...) {
		if field == "" || seen[field] {
			continue
		}

		seen[field] = true

		all = append(all, field)
	}

	sort.Strings(all)

	payload := models.JSONMap{}
	for key, value := range extra {
		payload[key] = value
	}

	payload[PayloadKeyChangedFields] = all

	if len(safe) > 0 {
		payload[PayloadKeyChanges] = safe
	}

	return payload
}
