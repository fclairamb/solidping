package registry_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/plugins"
)

var checkTypeCountRe = regexp.MustCompile(`(?i)\b(\d{2})\+?\s+check[- ]types?\b`)

// countMismatches returns one message per "NN check types" in content that
// differs from want.
func countMismatches(name, content string, want int) []string {
	var out []string

	for i, line := range strings.Split(content, "\n") {
		for _, m := range checkTypeCountRe.FindAllStringSubmatch(line, -1) {
			if n, _ := strconv.Atoi(m[1]); n != want {
				out = append(out, fmt.Sprintf("%s:%d says %d check types, expected %d", name, i+1, n, want))
			}
		}
	}

	return out
}

// scannedDocFiles lists the files (relative to root) that must carry the
// advertised check-type count.
func scannedDocFiles(t *testing.T, root string) []string {
	t.Helper()

	files := []string{"README.md", filepath.Join("web", "docs", "docusaurus.config.ts")}
	docs := filepath.Join(root, "web", "docs", "docs")

	err := filepath.WalkDir(docs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// changelog.md is generated from the root CHANGELOG.md (history).
		if ext := filepath.Ext(path); !d.IsDir() && (ext == ".md" || ext == ".mdx") &&
			path != filepath.Join(docs, "changelog.md") {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}

			files = append(files, rel)
		}

		return nil
	})
	require.NoError(t, err)

	return files
}

func scanFiles(t *testing.T, root string, files []string, want int) []string {
	t.Helper()

	out := make([]string, 0, len(files))

	for _, f := range files {
		content, err := os.ReadFile(filepath.Join(root, f))
		require.NoError(t, err, "reading %s", f)

		out = append(out, countMismatches(f, string(content), want)...)
	}

	return out
}

func TestAdvertisedCheckTypeCountInDocs(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	files := scannedDocFiles(t, root)

	require.Contains(t, files, "README.md")
	require.NotContains(t, files, "CHANGELOG.md")

	require.Empty(t, scanFiles(t, root, files, checkerdef.AdvertisedCheckTypeCount()),
		"bump the number in these docs (or set ExcludedFromTally on the new type)")
}

// Loading a plugin never changes the advertised count (spec 2026-10-17-05):
// plugin metas are always ExcludedFromTally.
func TestPluginsLeaveTheAdvertisedCountUnchanged(t *testing.T) {
	t.Parallel()

	before := checkerdef.AdvertisedCheckTypeCount()

	set := plugins.Load(t.Context(), filepath.Join(repoRoot(t), "plugins"), nil)
	t.Cleanup(set.Close)

	require.Empty(t, set.Errors)
	require.Contains(t, set.Types, checkerdef.CheckType("x-gopher"))
	require.NotNil(t, checkerdef.GetCheckTypeMeta("x-gopher"), "positive control: the plugin is loaded")
	require.Equal(t, before, checkerdef.AdvertisedCheckTypeCount())
	require.NotContains(t, checkerdef.AdvertisedCheckTypes(), checkerdef.CheckType("x-gopher"))
}

func TestCheckTypeCountMatcher(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want int // number of mismatches against 43
	}{
		{"43 check types", 0},
		{"**43 check types**", 0},
		{"39 Check Types", 1},
		{"40+ check types", 1},
		{"43+ check types", 0},
		{"see check-types", 0},
		{"the check types page", 0},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			require.Len(t, countMismatches("f.md", tt.in, 43), tt.want)
		})
	}
}

func TestCheckTypeCountFailureMessage(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "page.md"), []byte("ok\nWe have 40 check types\n"), 0o600))

	got := scanFiles(t, dir, []string{"page.md"}, 43)
	require.Equal(t, []string{"page.md:2 says 40 check types, expected 43"}, got)
}

// readmeCheckTypesRe matches a row of the README's "## Check types" table,
// shaped `| [Name](https://docs.solidping.io/features/<page>#<anchor>) | type | ... |`.
var readmeCheckTypesRe = regexp.MustCompile(
	"^\\| \\[[^]]+\\]\\(https://docs\\.solidping\\.io/features/([a-z-]+)(?:#([a-z0-9-]+))?\\) \\| `([a-z0-9_-]+)` \\|")

// readmeCheckTypeRow is one row of the README's check types table.
type readmeCheckTypeRow struct {
	page, anchor, checkType string
}

// readmeCheckTypeRows returns the rows of the README's "## Check types" table.
func readmeCheckTypeRows(readme string) []readmeCheckTypeRow {
	var rows []readmeCheckTypeRow

	inSection := false

	for _, l := range strings.Split(readme, "\n") {
		if strings.HasPrefix(l, "## ") {
			inSection = l == "## Check types"

			continue
		}

		if m := readmeCheckTypesRe.FindStringSubmatch(l); inSection && m != nil {
			rows = append(rows, readmeCheckTypeRow{page: m[1], anchor: m[2], checkType: m[3]})
		}
	}

	return rows
}

func TestReadmeCheckTypeTableMatchesRegistry(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "README.md"))
	require.NoError(t, err)

	rows := readmeCheckTypeRows(string(content))
	anchors := headingAnchorsInDocs(t, root)

	listed := make([]string, 0, len(rows))

	for _, r := range rows {
		listed = append(listed, r.checkType)

		if r.page == "check-types" {
			assert.True(t, anchors[r.anchor], "README links %s to missing check-types anchor #%s", r.checkType, r.anchor)
		} else {
			_, statErr := os.Stat(filepath.Join(root, "web", "docs", "docs", "features", r.page+".md"))
			require.NoError(t, statErr, "README links %s to missing docs page %s", r.checkType, r.page)
		}
	}

	want := make([]string, 0, checkerdef.AdvertisedCheckTypeCount())
	for _, ct := range checkerdef.AdvertisedCheckTypes() {
		want = append(want, string(ct))
	}

	require.ElementsMatch(t, want, listed, "README check types table must list every advertised check type once")
}

func TestReadmeCheckTypeRows(t *testing.T) {
	t.Parallel()

	readme := strings.Join([]string{
		"## Check types",
		"| Check | Type | What it checks |",
		"|---|---|---|",
		"| [TCP](https://docs.solidping.io/features/check-types#tcp) | `tcp` | Port |",
		"| [JavaScript](https://docs.solidping.io/features/javascript-checks) | `js` | Scripts |",
		"| TCP without a docs link | `udp` | not matched |",
		"## Inspiration",
		"| [TCP](https://docs.solidping.io/features/check-types#tcp) | `ssh` | outside the section |",
	}, "\n")

	require.Equal(t, []readmeCheckTypeRow{
		{page: "check-types", anchor: "tcp", checkType: "tcp"},
		{page: "javascript-checks", checkType: "js"},
	}, readmeCheckTypeRows(readme))
}
