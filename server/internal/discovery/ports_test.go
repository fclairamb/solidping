package discovery

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPortTableCoherence guards against drift between the authoritative
// defaultPorts table, the derived defaultPortList(), and the suggestion engine.
func TestPortTableCoherence(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	list := defaultPortList()
	r.Len(list, len(defaultPorts), "defaultPortList must have one entry per defaultPorts spec")

	for i, spec := range defaultPorts {
		r.Equal(spec.Port, list[i], "defaultPortList order must match defaultPorts")

		// Every port in the table must produce a suggestion (no silent drift).
		s := suggestForPort("1.2.3.4", "1.2.3.4", spec.Port, make(map[string]struct{}))
		r.NotNil(s, "port %d in defaultPorts must yield a suggestion", spec.Port)
		r.Equal(spec.CheckType, s.Type, "suggestion type must match the spec CheckType for port %d", spec.Port)

		var cfg map[string]any
		r.NoError(json.Unmarshal(s.Config, &cfg))

		switch spec.CheckType {
		case checkTypeHTTP:
			r.NotEmpty(spec.URLTmpl, "http port %d must define a URL template", spec.Port)
			r.Contains(cfg, "url")
		case checkTypeTCP:
			r.Empty(spec.URLTmpl, "tcp port %d must not define a URL template", spec.Port)
			r.Equal("1.2.3.4", cfg["host"])
			r.EqualValues(spec.Port, cfg["port"])
		default:
			r.Failf("unexpected check type", "port %d has unsupported type %q", spec.Port, spec.CheckType)
		}
	}
}

// TestSuggestForPortUnknown asserts ports absent from the table produce no
// suggestion.
func TestSuggestForPortUnknown(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Nil(suggestForPort("1.2.3.4", "1.2.3.4", 12345, make(map[string]struct{})))
}

// TestDefaultPortListMatchesSpec pins the exact default port set so an
// accidental edit to defaultPorts is caught.
func TestDefaultPortListMatchesSpec(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	want := []int{22, 25, 53, 80, 110, 143, 443, 465, 587, 993, 995, 3306, 5432, 6379, 8080, 8443}
	r.Equal(want, defaultPortList())
}
