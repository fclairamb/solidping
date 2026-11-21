package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// TestManifestWithOnlyNonCheckKinds pins spec 2026-11-21-01's manifest rule:
// "at least one kind is non-empty" replaces "checks is non-empty".
func TestManifestWithOnlyNonCheckKinds(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	body := []byte("version: 2\norganization: acme\nstatusPages:\n  - slug: status\n    name: Status\n")

	r.True(checks.IsDocumentBody(body), "a manifest of only status pages is a document")

	doc, err := checks.ParseManifest(body, "application/yaml")
	r.NoError(err)
	r.Len(doc.StatusPages, 1)
	r.False(doc.IsEmpty())
	r.False(doc.HasOnlyChecks())

	for _, issue := range checks.ValidateDocument(doc) {
		r.NotEqual(checks.CodeEmptyChecks, issue.Code)
	}

	empty := &checks.ExportDocument{Version: 2, Organization: "acme"}
	r.True(empty.IsEmpty())

	codes := map[string]bool{}
	for _, issue := range checks.ValidateDocument(empty) {
		codes[issue.Code] = true
	}

	r.True(codes[checks.CodeEmptyChecks])
}

// TestExportV2WireCarriesTheOtherKinds round-trips channels, windows and pages
// through the v2 wire format.
func TestExportV2WireCarriesTheOtherKinds(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	src := &checks.ExportDocument{
		Version: checks.ExportVersionV2, Organization: "acme",
		Channels:    []checks.ExportChannel{{Slug: "alerts", Name: "Alerts", Type: "ntfy"}},
		StatusPages: []checks.ExportStatusPage{{Slug: "status", Name: "Status"}},
	}

	raw, err := checks.MarshalExportDocument(src)
	r.NoError(err)

	got, err := checks.ParseManifest(raw, "application/json")
	r.NoError(err)
	r.Equal(src.Channels, got.Channels)
	r.Equal(src.StatusPages, got.StatusPages)
	r.Empty(got.MaintenanceWindows)
}
