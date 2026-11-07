package checkfilter_test

import (
	"strings"
	"testing"

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
