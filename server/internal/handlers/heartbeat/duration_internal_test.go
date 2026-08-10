package heartbeat

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestExtractHeartbeatDurationNaNAndInfIgnored exercises extractHeartbeatDuration
// directly (white-box, same package) because a genuine NaN or Infinity value
// has no valid JSON wire representation — encoding/json rejects both "NaN"
// and out-of-range exponents as malformed tokens before a map[string]any is
// ever produced. The guard is still real defensive code (a body decoded via
// a future json.Number/UseNumber path, or any other caller of this function,
// could hand it a non-finite float64), so it is tested at the unit level
// rather than left unverified.
func TestExtractHeartbeatDurationNaNAndInfIgnored(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cases := map[string]float64{
		"NaN":      math.NaN(),
		"+Inf":     math.Inf(1),
		"-Inf":     math.Inf(-1),
		"negative": -1,
		"overCap":  maxHeartbeatDurationMs + 1,
	}

	for name, val := range cases {
		body := map[string]any{"durationMs": val}
		duration := extractHeartbeatDuration(body)

		r.InDelta(float32(0), duration, 0.0001, name)
		r.Contains(body, "durationMs", "invalid value must be left in body: "+name)
	}
}

func TestExtractHeartbeatDurationValidConsumesKey(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	body := map[string]any{"durationMs": float64(42000), "other": "kept"}
	duration := extractHeartbeatDuration(body)

	r.InEpsilon(float32(42000), duration, 0.0001)
	r.NotContains(body, "durationMs", "valid value must be consumed")
	r.Contains(body, "other")
}

func TestExtractHeartbeatDurationBoundaryAtCapAccepted(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	body := map[string]any{"durationMs": float64(maxHeartbeatDurationMs)}
	duration := extractHeartbeatDuration(body)

	r.InEpsilon(float32(maxHeartbeatDurationMs), duration, 0.0001)
	r.NotContains(body, "durationMs")
}

func TestExtractHeartbeatDurationAbsentReturnsZero(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	body := map[string]any{}
	duration := extractHeartbeatDuration(body)

	r.InDelta(float32(0), duration, 0.0001)
}

func TestExtractHeartbeatDurationWrongTypeIgnored(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	body := map[string]any{"durationMs": "42000"}
	duration := extractHeartbeatDuration(body)

	r.InDelta(float32(0), duration, 0.0001)
	r.Contains(body, "durationMs", "wrong-typed value must be left in body")
}
