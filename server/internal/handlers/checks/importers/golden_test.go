package importers_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/checks/importers"
)

// updateGolden regenerates the golden snapshots instead of asserting against
// them: `go test ./internal/handlers/checks/importers/ -update`.
var updateGolden = flag.Bool("update", false, "update the importer golden files")

// goldenSnapshot is the stable, serialized form of a conversion: the produced
// export document plus every warning, in source order.
type goldenSnapshot struct {
	Document *checks.ExportDocument        `json:"document"`
	Warnings []importers.ConversionWarning `json:"warnings"`
}

// assertGolden compares a conversion result against testdata/<name>.golden.json.
// ExportedAt is zeroed first — it is wall-clock and informational only.
func assertGolden(t *testing.T, name string, result *importers.ConversionResult) {
	t.Helper()
	r := require.New(t)

	doc := *result.Document
	doc.ExportedAt = ""

	snapshot := goldenSnapshot{Document: &doc, Warnings: result.Warnings}

	actual, err := json.MarshalIndent(snapshot, "", "  ")
	r.NoError(err)

	actual = append(actual, '\n')

	path := filepath.Join("testdata", name+".golden.json")

	if *updateGolden {
		r.NoError(os.WriteFile(path, actual, 0o600))

		return
	}

	expected, err := os.ReadFile(path) //nolint:gosec // fixed test-data path
	r.NoError(err, "missing golden file %s — run the test with -update", path)
	r.JSONEq(string(expected), string(actual))
}

// readFixture loads a raw source fixture from testdata.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // fixed test-data path
	require.NoError(t, err)

	return data
}
