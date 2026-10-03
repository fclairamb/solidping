// Package formats parses the health documents applications already expose
// (Spring Boot Actuator, ASP.NET Core HealthChecks, spatie/laravel-health, the
// IETF health-check draft, MicroProfile Health) into one common Report.
//
// Detection is on the JSON shape, never on the URL. Each format lives in its own
// file and registers itself in All, in the order `auto` tries them.
package formats

import (
	"errors"
	"sort"
	"strings"
	"time"
)

// Format names, as written in the check's `format` config key.
const (
	NameAuto         = "auto"
	NameSpatie       = "spatie"
	NameSpring       = "spring"
	NameIETF         = "ietf"
	NameASPNet       = "aspnet"
	NameMicroProfile = "microprofile"
	NameSimple       = "simple"
)

// ErrNotRecognised is returned by Parse when the document does not have the
// shape of the format.
var ErrNotRecognised = errors.New("health response not recognised")

// ComponentStatus is the normalised state of one component.
type ComponentStatus string

// Component statuses.
const (
	StatusOK      ComponentStatus = "ok"
	StatusWarning ComponentStatus = "warning"
	StatusFailed  ComponentStatus = "failed"
	StatusSkipped ComponentStatus = "skipped"
	StatusUnknown ComponentStatus = "unknown"
)

// Report is the common model every format is parsed into.
type Report struct {
	Format string
	// Overall is the status the application reported for itself, when it did.
	Overall ComponentStatus
	// FinishedAt is when the results were computed (spatie finishedAt, ietf
	// time), nil when the format carries no timestamp.
	FinishedAt *time.Time
	Components []Component
}

// Component is one dependency in a Report.
type Component struct {
	Name    string
	Label   string
	Message string
	Summary string
	Status  ComponentStatus
	Meta    map[string]any
}

// Format is one supported health document format.
type Format struct {
	Name string
	// Detect reports whether the document looks like this format.
	Detect func(contentType string, body map[string]any) bool
	// Parse turns the document into a Report, or returns ErrNotRecognised when
	// it lacks the format's required fields.
	Parse func(body map[string]any) (Report, error)
}

// All lists the formats in the order `auto` tries them.
var All = []Format{
	spatieFormat,
	springFormat,
	ietfFormat,
	aspnetFormat,
	microprofileFormat,
	simpleFormat,
}

// ByName returns the format with that name.
func ByName(name string) (Format, bool) {
	for _, format := range All {
		if format.Name == name {
			return format, true
		}
	}

	return Format{}, false
}

// Names lists every format name accepted by the `format` key, `auto` first.
func Names() []string {
	names := make([]string, 0, len(All)+1)
	names = append(names, NameAuto)

	for _, format := range All {
		names = append(names, format.Name)
	}

	return names
}

// Detect returns the first format whose detection matches.
func Detect(contentType string, body map[string]any) (Format, bool) {
	for _, format := range All {
		if format.Detect(contentType, body) {
			return format, true
		}
	}

	return Format{}, false
}

// Parse parses a document with the named format (`auto` detects it).
func Parse(name, contentType string, body map[string]any) (Report, error) {
	if body == nil {
		return Report{}, ErrNotRecognised
	}

	if name == "" || name == NameAuto {
		format, ok := Detect(contentType, body)
		if !ok {
			return Report{}, ErrNotRecognised
		}

		return format.Parse(body)
	}

	format, ok := ByName(name)
	if !ok {
		return Report{}, ErrNotRecognised
	}

	return format.Parse(body)
}

// asString returns v as a string, "" when it is not one.
func asString(value any) string {
	text, _ := value.(string)

	return text
}

// asMap returns v as an object, nil when it is not one.
func asMap(value any) map[string]any {
	object, _ := value.(map[string]any)

	return object
}

// asSlice returns v as an array, nil when it is not one.
func asSlice(value any) []any {
	array, _ := value.([]any)

	return array
}

// sortedKeys returns the keys of a JSON object in a stable order, so the
// component order (and the capped metric selection) never depends on Go's map
// iteration.
func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

// primitiveMeta keeps the scalar values of an object (strings, numbers,
// booleans); nested structures are dropped.
func primitiveMeta(object map[string]any) map[string]any {
	meta := make(map[string]any)

	for key, value := range object {
		switch value.(type) {
		case string, float64, bool:
			meta[key] = value
		}
	}

	if len(meta) == 0 {
		return nil
	}

	return meta
}

// mapStatus maps a vendor status word (case-insensitive) through table;
// anything unlisted is StatusUnknown.
func mapStatus(raw string, table map[string]ComponentStatus) ComponentStatus {
	if status, ok := table[strings.ToLower(strings.TrimSpace(raw))]; ok {
		return status
	}

	return StatusUnknown
}

// parseTime reads a timestamp given as unix seconds, unix milliseconds or an
// RFC 3339 string.
func parseTime(value any) *time.Time {
	switch typed := value.(type) {
	case float64:
		if typed <= 0 {
			return nil
		}

		var parsed time.Time
		if typed > 1e11 {
			parsed = time.UnixMilli(int64(typed)).UTC()
		} else {
			parsed = time.Unix(int64(typed), 0).UTC()
		}

		return &parsed
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
			if parsed, err := time.Parse(layout, typed); err == nil {
				parsed = parsed.UTC()

				return &parsed
			}
		}
	}

	return nil
}
