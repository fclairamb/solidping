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

// readmeListCount counts the check types enumerated on the README's
// "- **N check types**: a, b, ..." line, expanding the grouped forms.
func readmeListCount(t *testing.T, readme string) int {
	t.Helper()

	var line string

	for _, l := range strings.Split(readme, "\n") {
		if regexp.MustCompile(`^- \*\*\d+ check types\*\*:`).MatchString(l) {
			line = l[strings.Index(l, ":")+1:]
		}
	}

	require.NotEmpty(t, line, "README check types line not found")

	total := 0

	for _, item := range splitTopLevel(line) {
		item = strings.TrimSpace(item)
		open := strings.Index(item, "(")
		head, inner := item, ""

		if open >= 0 {
			head, inner = strings.TrimSpace(item[:open]), item[open+1:strings.LastIndex(item, ")")]
		}

		switch {
		case regexp.MustCompile(`^\d+ `).MatchString(head):
			n, _ := strconv.Atoi(strings.Fields(head)[0])
			total += n
		case strings.Contains(inner, ","):
			total += len(strings.Split(inner, ","))
		default:
			total += len(strings.Split(head, "/"))
		}
	}

	return total
}

// splitTopLevel splits on commas that are outside parentheses.
func splitTopLevel(s string) []string {
	var out []string

	depth, start := 0, 0

	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}

	return append(out, s[start:])
}

func TestReadmeCheckTypeListMatchesCount(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile(filepath.Join(repoRoot(t), "README.md"))
	require.NoError(t, err)

	require.Equal(t, checkerdef.AdvertisedCheckTypeCount(), readmeListCount(t, string(content)))
}
