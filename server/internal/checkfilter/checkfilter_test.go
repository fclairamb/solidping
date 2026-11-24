package checkfilter_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkfilter"
)

func TestSplitCapsTheRawList(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ids, err := checkfilter.Split("")
	r.NoError(err)
	r.Nil(ids)

	atCap := strings.Repeat("a,", checkfilter.MaxIdentifiers-1) + "a"
	ids, err = checkfilter.Split(atCap)
	r.NoError(err)
	r.Len(ids, checkfilter.MaxIdentifiers)

	// Empty entries count: the cap applies before trimming or dedup.
	_, err = checkfilter.Split(strings.Repeat(",", checkfilter.MaxIdentifiers))
	r.ErrorIs(err, checkfilter.ErrTooManyIdentifiers)

	_, err = checkfilter.Split(atCap + ",a")
	r.ErrorIs(err, checkfilter.ErrTooManyIdentifiers)

	r.Contains(checkfilter.ErrTooManyIdentifiers.Error(), "checkUid")
}

func TestCheckCount(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.NoError(checkfilter.CheckCount(make([]string, checkfilter.MaxIdentifiers)))
	r.ErrorIs(checkfilter.CheckCount(make([]string, checkfilter.MaxIdentifiers+1)),
		checkfilter.ErrTooManyIdentifiers)
}

func TestNormalizeTrimsDedupsAndKeepsOrder(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal([]string{"b", "a", "c"},
		checkfilter.Normalize([]string{" b", "a", "", "b ", "  ", "c", "a"}))
	r.Empty(checkfilter.Normalize(nil))
}

func TestNormalizeTruncatesAsABackstop(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ids := make([]string, checkfilter.MaxIdentifiers+5)
	for idx := range ids {
		ids[idx] = strings.Repeat("x", idx+1)
	}

	r.Len(checkfilter.Normalize(ids), checkfilter.MaxIdentifiers)
}

func TestSearchTermsCapsWords(t *testing.T) {
	t.Parallel()

	words := make([]string, 10000)
	for i := range words {
		words[i] = "w" + strings.Repeat("x", i%7) + string(rune('a'+i%26))
	}

	// Distinct words: use numbers to be sure.
	distinct := make([]string, 10000)
	for i := range distinct {
		distinct[i] = "w" + strings.Repeat("1", i%5+1) + strings.Repeat("2", i/5%5+1)
	}

	require.Len(t, checkfilter.SearchTerms(strings.Join(words, " ")), 8)
	require.LessOrEqual(t, len(checkfilter.SearchTerms(strings.Join(distinct, " "))), 8)
}

func TestSearchTermsManyWordsExactlyEight(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	for i := range 10000 {
		sb.WriteString("a")
		sb.WriteString(strings.Repeat("b", i%200))
		sb.WriteString(" ")
	}

	require.Len(t, checkfilter.SearchTerms(sb.String()), 8)
}

func TestSearchTermsTruncatesOnRuneBoundary(t *testing.T) {
	t.Parallel()

	got := checkfilter.SearchTerms(strings.Repeat("é", 5000))
	require.Len(t, got, 1)
	require.Len(t, got[0], 256)
	require.True(t, utf8.ValidString(got[0]))

	got = checkfilter.SearchTerms("a" + strings.Repeat("é", 5000))
	require.Len(t, got[0], 255)
	require.True(t, utf8.ValidString(got[0]))
}

func TestSearchTermsDedupAndEmpty(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{"acme", "api"}, checkfilter.SearchTerms("acme  api acme\tapi"))
	require.Empty(t, checkfilter.SearchTerms("   "))
	require.Empty(t, checkfilter.SearchTerms(""))
}
