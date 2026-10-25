package plugins

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNumericAcceptsEveryNumberShape(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, value := range []any{float64(2), float32(2), 2, int64(2), int32(2), uint64(2), json.Number("2")} {
		got, ok := numeric(value)
		r.True(ok, "%T", value)
		r.InDelta(2.0, got, 1e-9)
	}

	for _, value := range []any{json.Number("x"), "2", nil} {
		_, ok := numeric(value)
		r.False(ok, "%T", value)
	}
}
