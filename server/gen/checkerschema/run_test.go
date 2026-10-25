package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRunWritesChecksAndPrunes drives run end to end in a scratch directory:
// a write pass, a clean -check pass, then the stale, orphan and missing cases.
func TestRunWritesChecksAndPrunes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	dir := t.TempDir()
	orphan := filepath.Join(dir, "removed-type.json")
	r.NoError(os.WriteFile(orphan, []byte("{}"), 0o600))

	r.NoError(run(dir, false))
	_, err := os.Stat(orphan)
	r.True(os.IsNotExist(err), "a schema for an unknown type is pruned")

	entries, err := filepath.Glob(filepath.Join(dir, "*.json"))
	r.NoError(err)
	r.NotEmpty(entries)

	r.NoError(run(dir, true))

	// Orphan file in -check mode.
	r.NoError(os.WriteFile(orphan, []byte("{}"), 0o600))
	r.ErrorIs(run(dir, true), errOrphanSchema)
	r.NoError(os.Remove(orphan))

	// Stale file.
	r.NoError(os.WriteFile(entries[0], []byte("{}"), 0o600))
	r.ErrorIs(run(dir, true), errStaleSchema)

	// Missing file.
	r.NoError(os.Remove(entries[0]))
	r.ErrorContains(run(dir, true), "is missing")

	// Unwritable output directory.
	r.Error(run(filepath.Join(dir, "does", "not", "exist"), false))
}
