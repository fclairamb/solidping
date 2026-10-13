package checktypes

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestToResponseExcludedFromTally(t *testing.T) {
	t.Parallel()

	resp := toResponse([]checkerdef.CheckTypeStatus{
		{CheckTypeMeta: *checkerdef.GetCheckTypeMeta(checkerdef.CheckTypeSleep)},
		{CheckTypeMeta: *checkerdef.GetCheckTypeMeta(checkerdef.CheckTypePrivateLocation)},
		{CheckTypeMeta: *checkerdef.GetCheckTypeMeta(checkerdef.CheckTypeHTTP)},
	})

	got := map[string]bool{}
	for _, d := range resp.Data {
		got[d.Type] = d.ExcludedFromTally
	}

	require.Equal(t, map[string]bool{"sleep": true, "private-location": true, "http": false}, got)
}
