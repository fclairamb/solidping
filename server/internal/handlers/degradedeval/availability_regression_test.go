package degradedeval_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/availability"
)

// TestDegradedIncidentIsNotDowntime is the availability regression the spec
// names as the most likely one in the whole change: a degraded incident leaking
// into an availability, SLO or uptime-report denominator.
//
// Asserted against the real availability service and a real open degraded
// incident, not by reading the filter's source.
func TestDegradedIncidentIsNotDowntime(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newEvalSetup(t, nil)

	s.up(t, 10, 100)

	for range 7 {
		s.down(t)
		s.up(t, 4, 100)
	}

	s.evaluate(t)

	open := s.activeDegraded(t)
	r.NotNil(open, "the fixture only means something with a degraded incident open")

	availabilitySvc := availability.NewService(s.dbSvc, &config.Config{})

	response, err := availabilitySvc.GetAvailability(
		t.Context(), s.org.Slug, s.check.UID, &availability.GetAvailabilityOptions{Periods: []string{"7d"}},
	)
	r.NoError(err)
	r.Len(response.Data, 1)

	period := response.Data[0]

	r.Equal(0, period.Incidents.Count,
		"an open degraded incident must not be counted as an outage")
	r.EqualValues(0, period.Incidents.TotalDowntimeSeconds,
		"and must contribute no wall-clock downtime")

	// The probe-ratio half is unaffected either way (it reads results, not
	// incidents), but it is worth pinning that the 7 real failures ARE still
	// counted there — the feature must not have made the failures disappear.
	r.True(period.HasData)
	r.NotNil(period.AvailabilityPct)
	r.Less(*period.AvailabilityPct, 100.0)
}

// downtimeFilterFiles are every place that builds a ListIncidentsFilter in order
// to derive DOWNTIME or public state from incidents. Each one must restrict to
// kind='check', or a degraded incident (and a burn alert before it) would count
// against the very availability it is reporting on.
//
// A source-level guard rather than four more integration tests, deliberately:
// the failure mode is a NEW call site added later by somebody who never read
// this spec, and a test that enumerates today's call sites cannot catch that. A
// parser over the files that compute downtime can.
var downtimeFilterFiles = map[string]string{ //nolint:gochecknoglobals // test fixture table
	"../../uptimereport/report.go":                "the uptime report's incident section",
	"../availability/service.go":                  "the availability API's downtime block",
	"../slos/service.go":                          "the SLO status endpoint's incident context",
	"../incidentpublications/group.go":            "status-page consolidation of group members",
}

// TestEveryDowntimeFilterRestrictsKind parses those files and requires every
// models.ListIncidentsFilter composite literal in them to set Kinds.
func TestEveryDowntimeFilterRestrictsKind(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checked := 0

	for path, what := range downtimeFilterFiles {
		fset := token.NewFileSet()

		file, err := parser.ParseFile(fset, path, nil, 0)
		r.NoErrorf(err, "parsing %s (%s)", path, what)

		ast.Inspect(file, func(node ast.Node) bool {
			lit, ok := node.(*ast.CompositeLit)
			if !ok || !isListIncidentsFilter(lit.Type) {
				return true
			}

			checked++

			r.Truef(hasField(lit, "Kinds"),
				"%s:%d (%s): this ListIncidentsFilter computes downtime or public state but does "+
					"not set Kinds, so a degraded incident (kind=%q) or a burn alert would be counted "+
					"as an outage. Pass Kinds: []string{models.IncidentKindCheck}.",
				path, fset.Position(lit.Pos()).Line, what, models.IncidentKindDegraded)

			return true
		})
	}

	r.NotZero(checked, "found no ListIncidentsFilter literals at all — the paths above have moved")
}

// isListIncidentsFilter matches `models.ListIncidentsFilter` and the
// same-package spelling.
func isListIncidentsFilter(expr ast.Expr) bool {
	switch typed := expr.(type) {
	case *ast.SelectorExpr:
		return typed.Sel.Name == "ListIncidentsFilter"
	case *ast.Ident:
		return typed.Name == "ListIncidentsFilter"
	case *ast.StarExpr:
		return isListIncidentsFilter(typed.X)
	default:
		return false
	}
}

// hasField reports whether a composite literal sets the named field.
func hasField(lit *ast.CompositeLit, name string) bool {
	for _, element := range lit.Elts {
		kv, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}

		if ident, isIdent := kv.Key.(*ast.Ident); isIdent && strings.EqualFold(ident.Name, name) {
			return true
		}
	}

	return false
}
