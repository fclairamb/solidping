package statuspages

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCreateStatusPageAndSectionSlugMaxLengthPersistsToDB is an end-to-end
// check (through the real SQLite migration chain, not just the in-memory
// validateSlug regex) that 100-char slugs actually persist for status pages
// and sections. Both slug columns previously carried a DB-level
// `length(slug) <= 40` CHECK constraint (migration 001) that drifted from,
// and would silently reject, the widened Go-level slugRegex — migration 009
// rebuilds both tables with `<= 100` to fix this (spec 2026-08-04-01).
func TestCreateStatusPageAndSectionSlugMaxLengthPersistsToDB(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupStatusPagesTest(t)

	pageSlug := "a" + strings.Repeat("b", 99)
	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{Name: "Long", Slug: pageSlug})
	r.NoError(err)
	r.Equal(pageSlug, page.Slug)

	sectionSlug := "c" + strings.Repeat("d", 99)
	section, err := svc.CreateSection(ctx, org.Slug, page.UID,
		CreateSectionRequest{Name: "Long Section", Slug: sectionSlug})
	r.NoError(err)
	r.Equal(sectionSlug, section.Slug)
}
