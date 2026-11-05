package checks

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestUniqueCheckUIDsIsLinear: the dedup must not be quadratic (security scan
// 2026-11-05, AE1): 200,000 distinct UIDs took tens of seconds with a
// slices.Contains loop.
func TestUniqueCheckUIDsIsLinear(t *testing.T) {
	t.Parallel()

	uids := make([]string, 200_000)
	for idx := range uids {
		uids[idx] = strconv.Itoa(idx)
	}

	start := time.Now()
	out := uniqueCheckUIDs(uids)
	elapsed := time.Since(start)

	require.Len(t, out, len(uids))
	require.Less(t, elapsed, 200*time.Millisecond, "dedup of 200,000 UIDs took %s", elapsed)
}

func TestUniqueCheckUIDsKeepsOrder(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{"b", "a", "c"}, uniqueCheckUIDs([]string{"b", "", "a", "b", "c", "a", ""}))
}
