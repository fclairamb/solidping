package discovery

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToInt_AcceptsEveryNumericShape(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal(3, toInt(float64(3)))
	r.Equal(4, toInt(4))
	r.Equal(5, toInt(int64(5)))
	r.Equal(6, toInt(json.Number("6")))
	r.Equal(0, toInt(json.Number("x")))
	r.Equal(0, toInt("7"))
	r.Equal(0, toInt(nil))
}
