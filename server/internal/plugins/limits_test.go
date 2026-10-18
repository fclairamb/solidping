package plugins

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/plugins/protocol"
)

func TestLimitResultOversizedOutput(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	res := LimitResult(&checkerdef.Result{
		Status: checkerdef.StatusUp,
		Output: map[string]any{
			"message": "short summary",
			"body":    strings.Repeat("é", 3*MaxOutputBytes),
			"list":    []any{strings.Repeat("x", 2*MaxOutputBytes)},
		},
	})

	r.Equal(checkerdef.StatusUp, res.Status)
	r.Equal("short summary", res.Output["message"])
	r.LessOrEqual(jsonSize(res.Output), MaxOutputBytes)
	r.Contains(fmt.Sprint(res.Output[OutputKeyTruncated]), "output over")

	body, ok := res.Output["body"].(string)
	r.True(ok, "a long string is shortened, not dropped")
	r.NotEmpty(body)
	r.True(strings.HasPrefix(strings.Repeat("é", 3*MaxOutputBytes), body), "cut on a rune boundary")
}

func TestLimitResultMetrics(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	metrics := map[string]any{"text": "fast", "flag": true, "nested": map[string]any{"a": 1}}
	for i := range MaxMetrics + 5 {
		metrics[fmt.Sprintf("m%03d", i)] = float64(i)
	}

	res := LimitResult(&checkerdef.Result{Status: checkerdef.StatusUp, Metrics: metrics})

	r.Len(res.Metrics, MaxMetrics)
	r.NotContains(res.Metrics, "text")
	r.NotContains(res.Metrics, "flag")

	cut := fmt.Sprint(res.Output[OutputKeyTruncated])
	r.Contains(cut, `metric "text" is not numeric`)
	r.Contains(cut, `metric "nested" is not numeric`)
	r.Contains(cut, "5 metrics over the 32 cap")
}

func TestLimitResultUnknownStatus(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	res := LimitResult(&checkerdef.Result{Status: checkerdef.StatusDegraded})
	r.Equal(checkerdef.StatusError, res.Status)
	r.Contains(fmt.Sprint(res.Output[OutputKeyTruncated]), "not a known status")

	res = LimitResult(&checkerdef.Result{Status: checkerdef.Status(42)})
	r.Equal(checkerdef.StatusError, res.Status)

	// A protocol answer with an unknown status string is an error naming it.
	mapped := resultFromProtocol(&protocol.ExecuteResult{Status: "great"}, 0)
	r.Equal(checkerdef.StatusError, mapped.Status)
	r.Contains(fmt.Sprint(mapped.Output["error"]), `"great"`)

	// Positive control: a clean result passes untouched.
	clean := LimitResult(&checkerdef.Result{
		Status: checkerdef.StatusWarning, Output: map[string]any{"message": "ok"}, Metrics: map[string]any{"a": 1},
	})
	r.Equal(checkerdef.StatusWarning, clean.Status)
	r.NotContains(clean.Output, OutputKeyTruncated)
	r.InDelta(1, clean.Metrics["a"], 0)
}
