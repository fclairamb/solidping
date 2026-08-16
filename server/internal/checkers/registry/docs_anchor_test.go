package registry_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// docsAnchorExemptTypes lists check types that are intentionally exempt from
// the docs-anchor sync check below: `sleep` is a synthetic/testing check type
// (see server/internal/checkers/CLAUDE.md) with no docs section and is never
// customer-facing.
//
//nolint:gochecknoglobals // test lookup table
var docsAnchorExemptTypes = map[checkerdef.CheckType]bool{
	checkerdef.CheckTypeSleep: true,
}

// repoRoot resolves the repository root from this test file's own location,
// independent of the working directory go test is invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")

	// This file lives at <root>/server/internal/checkers/registry/docs_anchor_test.go.
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")
}

// headingAnchorsInDocs parses `### Heading {#anchor}` lines out of the
// check-types docs page and returns the set of pinned anchors.
func headingAnchorsInDocs(t *testing.T, root string) map[string]bool {
	t.Helper()

	path := filepath.Join(root, "web", "docs", "docs", "features", "check-types.md")

	content, err := os.ReadFile(path)
	require.NoError(t, err, "reading %s", path)

	re := regexp.MustCompile(`(?m)^###\s.*\{#([a-z0-9-]+)\}\s*$`)
	matches := re.FindAllStringSubmatch(string(content), -1)

	anchors := make(map[string]bool, len(matches))
	for _, m := range matches {
		anchors[m[1]] = true
	}

	return anchors
}

// frontendAnchorMap parses the `type: "anchor"` entries out of the dash0
// type→anchor map, returning type -> anchor.
func frontendAnchorMap(t *testing.T, root string) map[string]string {
	t.Helper()

	path := filepath.Join(
		root, "web", "dash0", "src", "components", "shared", "check-type-docs-anchors.ts",
	)

	content, err := os.ReadFile(path)
	require.NoError(t, err, "reading %s", path)

	// Matches lines like:  http: "httphttps",
	re := regexp.MustCompile(`(?m)^\s*([a-z0-9_]+):\s*"([a-z0-9-]+)",\s*$`)
	matches := re.FindAllStringSubmatch(string(content), -1)

	require.NotEmpty(t, matches, "no type: \"anchor\" entries found in %s — regex drifted from the file format", path)

	result := make(map[string]string, len(matches))
	for _, m := range matches {
		result[m[1]] = m[2]
	}

	return result
}

// TestCheckTypeDocsAnchors_SyncedWithRegistryAndDocs is the drift-detecting
// sync test required by the spec: every monitorable check type in the
// registry must have (a) a matching entry in the frontend type→anchor map,
// and (b) a matching `{#anchor}` heading in check-types.md. It also asserts
// there are no STALE entries in either direction, so a renamed/removed type
// is caught too.
func TestCheckTypeDocsAnchors_SyncedWithRegistryAndDocs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	root := repoRoot(t)

	docsAnchors := headingAnchorsInDocs(t, root)
	feAnchors := frontendAnchorMap(t, root)

	registryTypes := checkerdef.ListCheckTypeMetas()
	r.NotEmpty(registryTypes, "registry unexpectedly empty")

	seenInRegistry := make(map[string]bool, len(registryTypes))

	for _, meta := range registryTypes {
		checkType := string(meta.Type)
		seenInRegistry[checkType] = true

		if docsAnchorExemptTypes[meta.Type] {
			r.NotContains(feAnchors, checkType,
				"exempt type %q should not appear in the frontend anchor map", checkType)

			continue
		}

		anchor, ok := feAnchors[checkType]
		r.True(ok, "check type %q has no entry in the frontend docs-anchor map "+
			"(web/dash0/src/components/shared/check-type-docs-anchors.ts)", checkType)

		r.True(docsAnchors[anchor],
			"check type %q maps to anchor %q, but no `### Heading {#%s}` was found in "+
				"web/docs/docs/features/check-types.md", checkType, anchor, anchor)
	}

	// Catch stale entries: a frontend map entry for a type no longer in the registry.
	for checkType := range feAnchors {
		r.True(seenInRegistry[checkType],
			"frontend docs-anchor map has an entry for %q, which is not a registered check type", checkType)
	}
}
