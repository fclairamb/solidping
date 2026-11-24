package postgres

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
)

func TestApplyCheckSearchFilterCapsTerms(t *testing.T) {
	t.Parallel()

	database := bun.NewDB(&sql.DB{}, pgdialect.New())
	build := func(q string) string {
		return applyCheckSearchFilter(database.NewSelect().TableExpr("checks"), q).String()
	}

	// 5,000 words must produce at most 8 name/slug LIKE pairs.
	words := make([]string, 5000)
	for i := range words {
		words[i] = "w" + strings.Repeat("a", i%40) + string(rune('a'+i%26))
	}

	sqlText := build(strings.Join(words, " "))
	require.LessOrEqual(t, strings.Count(sqlText, "LIKE"), 16)
	require.Less(t, len(sqlText), 4096)

	// Positive control: three words give three pairs; empty adds none.
	require.Equal(t, 6, strings.Count(build("acme api prod"), "LIKE"))
	require.NotContains(t, build(""), "LIKE")
}
