package incidentpublications_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentpublications"
)

// isForbiddenPublicKey reports JSON keys that must never appear on a public
// publication payload or a subscriber webhook. incidents.details can carry the
// probe's captured failure response (spec 2026-08-20-01): response bodies with
// internal hostnames, stack traces or PII. It is operator-facing evidence, not
// status-page content.
func isForbiddenPublicKey(name string) bool {
	switch name {
	case "details", "failureresponse", "first_result", "last_failure",
		"failure_reason", "output", "diagnostics":
		return true
	default:
		return false
	}
}

// TestPublicPublicationShapesCarryNoIncidentDetails walks the public
// publication payloads (the activeIncidents[] element rendered on status pages
// and the subscriber webhook body) and fails if any field could carry incident
// details, by name or by untyped-map type.
func TestPublicPublicationShapesCarryNoIncidentDetails(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	roots := map[string]reflect.Type{
		"PublicIncident":       reflect.TypeOf(incidentpublications.PublicIncident{}),
		"PublicIncidentUpdate": reflect.TypeOf(incidentpublications.PublicIncidentUpdate{}),
		"WebhookPayload":       reflect.TypeOf(incidentpublications.WebhookPayload{}),
	}

	seen := map[reflect.Type]bool{}
	visited := 0

	var walk func(typ reflect.Type, path string)

	walk = func(typ reflect.Type, path string) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
			typ = typ.Elem()
		}

		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}

		seen[typ] = true

		for i := range typ.NumField() {
			field := typ.Field(i)
			visited++

			name := strings.ToLower(strings.Split(field.Tag.Get("json"), ",")[0])
			if name == "" {
				name = strings.ToLower(field.Name)
			}

			r.False(isForbiddenPublicKey(name),
				"%s.%s (json %q) would publish incident details", path, field.Name, name)

			r.NotEqual(reflect.TypeOf(models.JSONMap{}), field.Type,
				"%s.%s is an untyped map that could carry incident details", path, field.Name)
			r.NotEqual(reflect.TypeOf(models.Incident{}), field.Type,
				"%s.%s embeds the whole incident, details included", path, field.Name)

			walk(field.Type, path+"."+field.Name)
		}
	}

	for name, typ := range roots {
		walk(typ, name)
	}

	// Positive control: the walk saw a real graph, so the assertions above are
	// not vacuously true.
	r.Greater(visited, 15)
	r.True(seen[reflect.TypeOf(incidentpublications.PublicIncidentUpdate{})])
}
