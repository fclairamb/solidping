// Package importers converts third-party monitoring configurations (Gatus,
// Better Stack, Uptime Kuma) into SolidPing's portable ExportDocument, which is
// then fed through the existing apply/import pipeline.
//
// Converters are pure functions: bytes in, ExportDocument + warnings out. They
// never touch the database — validation, group auto-creation and slug upsert
// are already handled by checks.Service.ApplyChecks. The one exception is the
// Better Stack converter, which performs an outbound HTTPS fetch against the
// Better Stack API (its "input" is an API token rather than a file).
//
// See mapping.md for the per-source field mapping tables.
package importers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhttp"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// Source identifiers accepted by the convert endpoint.
const (
	// SourceGatus is the Gatus config-as-YAML source.
	SourceGatus = "gatus"
	// SourceBetterStack is the Better Stack (Uptime) REST API source.
	SourceBetterStack = "betterstack"
	// SourceUptimeKuma is the Uptime Kuma 1.x backup-JSON source.
	SourceUptimeKuma = "uptime-kuma"
	// SourceUptimeRobot is the UptimeRobot API v2 getMonitors JSON source.
	SourceUptimeRobot = "uptimerobot"
)

// Config keys shared by several converters. Extracted so the same literal is
// not repeated across the three source mappings.
const (
	cfgKeyHost    = "host"
	cfgKeyPort    = "port"
	cfgKeyURL     = "url"
	cfgKeyTimeout = "timeout"
)

// Source-tool type/scheme tokens that several converters share.
const (
	srcTCP       = "tcp"
	srcPing      = "ping"
	srcKeyword   = "keyword"
	srcMongoDB   = "mongodb"
	srcPortType  = "port"
	srcRedis     = "redis"
	srcSQLServer = "sqlserver"
	srcHTTP      = "http"
	srcJSONQuery = "json-query"
)

// ErrUnknownSource is returned when the requested source has no converter.
var ErrUnknownSource = errors.New("unknown import source")

// ErrEmptyInput is returned when the supplied payload contains nothing to convert.
var ErrEmptyInput = errors.New("no importable items found in the input")

// ConversionWarning reports a single source item (or field) that could not be
// mapped faithfully. Warnings never block the import: whatever maps is
// imported, the rest is surfaced here.
type ConversionWarning struct {
	// Item names the source object the warning is about (endpoint / monitor
	// name). Empty for document-level warnings.
	Item string `json:"item,omitempty"`
	// Field names the source field, when the warning is field-scoped.
	Field string `json:"field,omitempty"`
	// Message is the human-readable explanation.
	Message string `json:"message"`
}

// ConversionResult is what a converter produces: a ready-to-apply document plus
// everything that did not map cleanly.
type ConversionResult struct {
	Document *checks.ExportDocument
	Warnings []ConversionWarning
}

// Converter turns one third-party format into an ExportDocument.
type Converter interface {
	// Source returns the source identifier ("gatus", "betterstack", …).
	Source() string
	// Convert parses input and produces a document plus warnings. A returned
	// error means the payload as a whole is unusable (bad YAML/JSON, empty);
	// individual unmappable items are reported as warnings instead.
	Convert(input []byte) (*ConversionResult, error)
}

// warnings accumulates conversion warnings in source order.
type warnings struct {
	list []ConversionWarning
}

// addf records an item-scoped warning.
func (w *warnings) addf(item, field, format string, args ...any) {
	w.list = append(w.list, ConversionWarning{
		Item:    item,
		Field:   field,
		Message: fmt.Sprintf(format, args...),
	})
}

// addDocf records a document-level warning (no source item).
func (w *warnings) addDocf(format string, args ...any) {
	w.addf("", "", format, args...)
}

// result returns the accumulated warnings, never nil, so the JSON response
// carries an empty array rather than null.
func (w *warnings) result() []ConversionWarning {
	if w.list == nil {
		return []ConversionWarning{}
	}

	return w.list
}

// slugInvalidChars matches every character that cannot appear in a check slug.
var slugInvalidChars = regexp.MustCompile(`[^a-z0-9-]`)

// maxSlugLen mirrors the checks service's slug cap (3-50 chars).
const maxSlugLen = 50

// minSlugLen mirrors the checks service's slug floor.
const minSlugLen = 3

// slugify converts a free-form source name into a slug that satisfies the
// checks service's `^[a-z][a-z0-9-]{2,49}$` rule. It mirrors sanitizeSlug in
// the checks package (which is unexported) so converted documents never fail
// validation on the slug alone.
func slugify(name string) string {
	slug := slugInvalidChars.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")

	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}

	slug = strings.Trim(slug, "-")

	// Must start with a lowercase letter.
	if slug == "" || slug[0] < 'a' || slug[0] > 'z' {
		slug = "x" + slug
	}

	if len(slug) > maxSlugLen {
		slug = strings.TrimRight(slug[:maxSlugLen], "-")
	}

	// Pad short slugs so they clear the 3-char floor.
	for len(slug) < minSlugLen {
		slug += "-x"
	}

	return slug
}

// slugSet dedupes slugs within one conversion by appending numeric suffixes,
// keeping the result stable across re-imports of the same source document (so
// the slug upsert stays idempotent).
type slugSet struct {
	seen map[string]int
}

func newSlugSet() *slugSet {
	return &slugSet{seen: make(map[string]int)}
}

// unique returns a slug derived from name that has not been handed out yet.
func (s *slugSet) unique(name string) string {
	base := slugify(name)

	count, exists := s.seen[base]
	if !exists {
		s.seen[base] = 1

		return base
	}

	// Append -2, -3, … until free, trimming the base so the total stays within
	// the 50-char cap.
	for {
		count++
		suffix := "-" + strconv.Itoa(count)

		candidate := base
		if len(candidate)+len(suffix) > maxSlugLen {
			candidate = strings.TrimRight(candidate[:maxSlugLen-len(suffix)], "-")
		}

		candidate += suffix
		if _, taken := s.seen[candidate]; !taken {
			s.seen[base] = count
			s.seen[candidate] = 1

			return candidate
		}
	}
}

// secondsToPeriod renders a whole number of seconds as a compact duration
// string ("30s", "5m", "1h"), which is what ExportCheck.Period expects. Zero or
// negative input yields "" so the document default applies.
func secondsToPeriod(seconds int) string {
	if seconds <= 0 {
		return ""
	}

	return durationToPeriod(time.Duration(seconds) * time.Second)
}

// durationToPeriod renders a duration in the compact form the export format
// uses. time.Duration.String() would produce "1m0s"; this trims the noise.
func durationToPeriod(duration time.Duration) string {
	if duration <= 0 {
		return ""
	}

	switch {
	case duration%time.Hour == 0:
		return strconv.FormatInt(int64(duration/time.Hour), 10) + "h"
	case duration%time.Minute == 0:
		return strconv.FormatInt(int64(duration/time.Minute), 10) + "m"
	case duration%time.Second == 0:
		return strconv.FormatInt(int64(duration/time.Second), 10) + "s"
	default:
		return duration.String()
	}
}

// intPtr is a tiny helper for the pointer-typed alerting fields on ExportCheck.
func intPtr(v int) *int {
	return &v
}

// newDocument builds the shell of an export document for a given source. The
// Organization field doubles as the managed-manifest name on apply, so each
// source gets its own scope ("gatus", "betterstack", "uptime-kuma") and a
// re-import of the same source updates in place instead of colliding with the
// org-wide manifest.
func newDocument(source string) *checks.ExportDocument {
	return &checks.ExportDocument{
		Version:      1,
		ExportedAt:   time.Now().UTC().Format(time.RFC3339),
		Organization: source,
		Checks:       []checks.ExportCheck{},
	}
}

// normalizeChecks post-processes a converted document so it satisfies the
// bounds the checks service enforces on apply. Every source tool allows values
// SolidPing does not, and an out-of-range value would otherwise fail the whole
// check as a per-check ImportError instead of importing it. Rather than drop
// the check, each value is clamped into range and the adjustment is reported
// as a warning — the same "import what maps, surface the rest" contract the
// converters follow for everything else.
//
// Three bounds are enforced, all of them real failures observed against real
// exports (an Uptime Kuma monitor carries a 48s timeout by default, and Better
// Stack allows multi-day heartbeat grace periods):
//   - the per-type minimum check period (checkerdef metadata),
//   - the uniform per-check `timeout` range (checks.Min/MaxConfigTimeout),
//   - the confirmation/recovery period cap (checks.MaxIncidentPeriodSeconds).
func normalizeChecks(doc *checks.ExportDocument, warn *warnings) {
	for i := range doc.Checks {
		check := &doc.Checks[i]

		normalizePeriod(check, warn)
		normalizeConfigTimeout(check, warn)
		normalizeIncidentPeriods(check, warn)
	}
}

// normalizePeriod raises a sub-minimum check period to the type's default.
func normalizePeriod(check *checks.ExportCheck, warn *warnings) {
	meta := checkerdef.GetCheckTypeMeta(checkerdef.CheckType(check.Type))
	if meta == nil || check.Period == "" {
		return
	}

	minPeriod := meta.MinPeriod
	if minPeriod == 0 {
		minPeriod = checkerdef.GlobalMinPeriod
	}

	period, err := time.ParseDuration(check.Period)
	if err == nil && period >= minPeriod {
		return
	}

	replacement := meta.DefaultPeriod
	if replacement < minPeriod {
		replacement = minPeriod
	}

	warn.addf(check.Name, "period",
		"period %q is below the minimum for %s checks; raised to %s",
		check.Period, check.Type, durationToPeriod(replacement))

	check.Period = durationToPeriod(replacement)
}

// normalizeConfigTimeout clamps the uniform per-check `timeout` config key into
// the 1s–30s range the checks service enforces for every check type. The key is
// type-agnostic: the worker reads it straight off the config map to size the
// execution budget (see checkworker.perCheckTimeout), so it is meaningful even
// for checkers whose own config struct has no Timeout field.
func normalizeConfigTimeout(check *checks.ExportCheck, warn *warnings) {
	raw, ok := check.Config[checks.ConfigTimeoutKey].(string)
	if !ok || raw == "" {
		return
	}

	timeout, err := time.ParseDuration(raw)
	if err != nil {
		warn.addf(check.Name, checks.ConfigTimeoutKey,
			"timeout %q is not a valid duration and was dropped; the check uses the default budget", raw)
		delete(check.Config, checks.ConfigTimeoutKey)

		return
	}

	clamped := timeout
	if clamped < checks.MinConfigTimeout {
		clamped = checks.MinConfigTimeout
	}

	if clamped > checks.MaxConfigTimeout {
		clamped = checks.MaxConfigTimeout
	}

	if clamped == timeout {
		return
	}

	warn.addf(check.Name, checks.ConfigTimeoutKey,
		"timeout %s is outside SolidPing's %s–%s range; clamped to %s",
		durationToPeriod(timeout), durationToPeriod(checks.MinConfigTimeout),
		durationToPeriod(checks.MaxConfigTimeout), durationToPeriod(clamped))

	check.Config[checks.ConfigTimeoutKey] = durationToPeriod(clamped)
}

// normalizeIncidentPeriods clamps the confirmation/recovery periods into the
// 0–86400s window the checks service accepts. Source tools are more permissive:
// a Better Stack heartbeat grace period or an Uptime Kuma
// maxretries × retryInterval product routinely exceeds a day.
func normalizeIncidentPeriods(check *checks.ExportCheck, warn *warnings) {
	check.ConfirmationPeriodSeconds = clampIncidentPeriod(
		check.Name, "confirmationPeriodSeconds", check.ConfirmationPeriodSeconds, warn)
	check.RecoveryPeriodSeconds = clampIncidentPeriod(
		check.Name, "recoveryPeriodSeconds", check.RecoveryPeriodSeconds, warn)
}

// clampIncidentPeriod bounds one incident period, warning when it had to move.
func clampIncidentPeriod(name, field string, seconds *int, warn *warnings) *int {
	if seconds == nil {
		return nil
	}

	value := *seconds
	switch {
	case value < 0:
		warn.addf(name, field, "%s was negative (%d) and was reset to 0", field, value)

		return intPtr(0)
	case value > checks.MaxIncidentPeriodSeconds:
		warn.addf(name, field,
			"%s of %s exceeds SolidPing's %s maximum; clamped to %s",
			field, durationToPeriod(time.Duration(value)*time.Second),
			durationToPeriod(checks.MaxIncidentPeriodSeconds*time.Second),
			durationToPeriod(checks.MaxIncidentPeriodSeconds*time.Second))

		return intPtr(checks.MaxIncidentPeriodSeconds)
	default:
		return seconds
	}
}

// assertionConfig renders a JSONPath assertion tree as the plain map the http
// checker's config validation expects (it rejects a typed struct value).
func assertionConfig(node *checkhttp.AssertionNode) map[string]any {
	encoded, err := json.Marshal(node)
	if err != nil {
		return nil
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil
	}

	return decoded
}

// withDefaultPort appends a default port to a bare host, for config fields that
// require the host:port form (the DNS checker's nameserver).
func withDefaultPort(host string, port int) string {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return ""
	}

	if _, _, err := net.SplitHostPort(trimmed); err == nil {
		return trimmed
	}

	return net.JoinHostPort(trimmed, strconv.Itoa(port))
}

// dnsDefaultPort is the port appended to a bare nameserver address.
const dnsDefaultPort = 53

// SupportedSources lists every source identifier the convert endpoint accepts.
func SupportedSources() []string {
	return []string{SourceGatus, SourceBetterStack, SourceUptimeKuma, SourceUptimeRobot}
}
