package checkjs

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNonFiniteNumbersStayEncodable: `Date.now() - undefined` is NaN, which
// JSON cannot encode. The metric is dropped and an output value becomes null,
// so the result can still be stored and sent.
func TestNonFiniteNumbersStayEncodable(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := runScript(t, `
var start;
return {
  status: "up",
  metrics: { loginMs: Date.now() - start, ratio: 1 / 0, okMs: 12 },
  output: { elapsed: Date.now() - start, nested: { values: [1, -1 / 0] }, label: "ok" },
};
`)

	r.Equal("up", result.Status.String())
	r.Equal(map[string]any{"okMs": int64(12)}, result.Metrics)
	r.Nil(result.Output["elapsed"])
	r.Equal("ok", result.Output["label"])

	_, err := json.Marshal(result.Metrics)
	r.NoError(err)
	_, err = json.Marshal(result.Output)
	r.NoError(err)
}
