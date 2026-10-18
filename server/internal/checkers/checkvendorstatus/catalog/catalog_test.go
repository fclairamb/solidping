package catalog_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkvendorstatus/catalog"
)

func TestCatalogIsWellFormed(t *testing.T) {
	t.Parallel()

	entries := catalog.All()
	require.GreaterOrEqual(t, len(entries), 40)

	seen := map[string]bool{}

	for _, entry := range entries {
		require.NotEmpty(t, entry.Slug)
		require.NotEmpty(t, entry.Name)
		require.NotEmpty(t, entry.Category)
		require.True(t, strings.HasPrefix(entry.URL, "https://"), entry.Slug)
		require.False(t, strings.HasSuffix(entry.URL, "/"), entry.Slug)
		require.False(t, seen[entry.Slug], "duplicate slug %s", entry.Slug)

		seen[entry.Slug] = true
	}

	entry, ok := catalog.Find("github")
	require.True(t, ok)
	require.Equal(t, "https://www.githubstatus.com", entry.URL)
}
