package base_test

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

func TestParseOrder(t *testing.T) {
	t.Parallel()

	for query, want := range map[string]bool{"": false, "order=desc": false, "order=asc": true} {
		values, err := url.ParseQuery(query)
		require.NoError(t, err)

		got, err := base.ParseOrder(values)
		require.NoError(t, err)
		require.Equal(t, want, got, query)
	}

	_, err := base.ParseOrder(url.Values{"order": {"sideways"}})
	require.ErrorIs(t, err, base.ErrInvalidOrder)
}

func TestCursorRoundTripAndLegacyUID(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.October, 1, 9, 0, 0, 123456789, time.UTC)
	ts, uid, ok := base.DecodeCursor(base.EncodeCursor(at, "abc"))
	require.True(t, ok)
	require.True(t, at.Equal(ts))
	require.Equal(t, "abc", uid)

	_, _, ok = base.DecodeCursor("3f2b9c1e-0000-4000-8000-000000000000")
	require.False(t, ok, "a bare UID is not a cursor")
}
