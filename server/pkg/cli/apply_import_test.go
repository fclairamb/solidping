package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFormatImportSummaryClean verifies a clean import's headline and exit
// code, in both real and dry-run mode.
func TestFormatImportSummaryClean(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := importResultSummary{Created: 3, Updated: 2, Skipped: 1}

	realRun := formatImportSummary(result, false)
	r.Equal("IMPORT: created=3 updated=2 skipped=1", realRun.headline)
	r.Empty(realRun.errorLines)
	r.Equal(0, realRun.exitCode)

	dry := formatImportSummary(result, true)
	r.Equal("DRY RUN: created=3 updated=2 skipped=1", dry.headline)
	r.Equal(0, dry.exitCode)
}

// TestFormatImportSummaryWithErrors verifies the per-item error lines
// ([index] slug: error) and the non-zero exit code the reference workflow's
// do_import contract requires.
func TestFormatImportSummaryWithErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := importResultSummary{Created: 1, Updated: 0, Skipped: 0}
	result.Errors = append(result.Errors, struct {
		Index int    `json:"index"`
		Slug  string `json:"slug"`
		Error string `json:"error"`
	}{Index: 2, Slug: "bad-check", Error: "invalid slug format"})

	summary := formatImportSummary(result, false)
	r.Equal("IMPORT: created=1 updated=0 skipped=0", summary.headline)
	r.Equal([]string{"[2] bad-check: invalid slug format"}, summary.errorLines)
	r.Equal(1, summary.exitCode)
}
