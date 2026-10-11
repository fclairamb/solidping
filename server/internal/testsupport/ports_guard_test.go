package testsupport

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fixedPortAssignment matches a hand-assigned test port: `const portX = 15xxx`,
// `portX = 15xxx` in a const block, or `Port: 15xxx` in a config literal.
var fixedPortAssignment = regexp.MustCompile(`(?:=|Port:)\s*(1[45]\d{3})\b`)

// TestFixedTestPortsAreUniqueAcrossPackages guards the hand-numbered ports the
// embedded-Postgres suites (and a few fake servers) listen on.
//
// Suites inside one package run one after another, so reusing a number there
// is harmless. Two PACKAGES are different story: `go test ./...` runs packages
// concurrently, and two that claim the same port fail with "Address already in
// use", or worse, one talks to the other's database. With `-p 1` that could
// never happen, so 16 numbers had quietly ended up shared by two packages when
// the Postgres layer first ran at -p 4 (2026-10-11).
func TestFixedTestPortsAreUniqueAcrossPackages(t *testing.T) {
	t.Parallel()

	root := moduleRoot(t)
	owners := map[int]map[string]bool{}

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			if name := entry.Name(); name == "node_modules" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}

			return nil
		}

		if !strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "ports_guard_test.go") {
			return nil
		}

		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		pkg, _ := filepath.Rel(root, filepath.Dir(path))

		for _, match := range fixedPortAssignment.FindAllSubmatch(src, -1) {
			port, _ := strconv.Atoi(string(match[1]))
			if owners[port] == nil {
				owners[port] = map[string]bool{}
			}

			owners[port][pkg] = true
		}

		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, owners, "the scan found no port at all: the pattern no longer matches how tests assign ports")

	var clashes []string

	for port, pkgs := range owners {
		if len(pkgs) < 2 {
			continue
		}

		names := make([]string, 0, len(pkgs))
		for pkg := range pkgs {
			names = append(names, pkg)
		}

		sort.Strings(names)
		clashes = append(clashes, strconv.Itoa(port)+": "+strings.Join(names, ", "))
	}

	sort.Strings(clashes)
	require.Empty(t, clashes, "test ports claimed by more than one package (pick an unused number):\n%s",
		strings.Join(clashes, "\n"))
}

// moduleRoot walks up from the working directory to the directory holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	require.NoError(t, err)

	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "go.mod not found above the test directory")
		dir = parent
	}
}
