package incidents

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestFailureReasonFromResult_PrefersOutputError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	status := int(models.ResultStatusDown)
	result := &models.Result{
		Status: &status,
		Output: models.JSONMap{checkerdef.OutputKeyError: "connection refused"},
	}

	r.Equal("connection refused", failureReasonFromResult(result))
}

func TestFailureReasonFromResult_FallsBackByStatus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status models.ResultStatus
		want   string
	}{
		{"timeout", models.ResultStatusTimeout, "timeout"},
		{"error", models.ResultStatusError, "error"},
		{"down", models.ResultStatusDown, "down"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			status := int(tc.status)
			result := &models.Result{Status: &status, Output: models.JSONMap{}}
			require.Equal(t, tc.want, failureReasonFromResult(result))
		})
	}
}

func TestFailureReasonFromResult_NoOutputNoStatus(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("down", failureReasonFromResult(&models.Result{}))
	r.Equal("down", failureReasonFromResult(nil))
}

func TestFailureReasonFromResult_EmptyErrorStringFallsBack(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	status := int(models.ResultStatusTimeout)
	result := &models.Result{
		Status: &status,
		Output: models.JSONMap{checkerdef.OutputKeyError: ""},
	}

	r.Equal("timeout", failureReasonFromResult(result))
}

func TestFailureDetails_Shape(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Now().Truncate(time.Second)
	status := int(models.ResultStatusDown)
	region := "eu"
	duration := float32(1.5)
	result := &models.Result{
		UID:         "result-1",
		Status:      &status,
		Region:      &region,
		Duration:    &duration,
		PeriodStart: now,
		Output:      models.JSONMap{checkerdef.OutputKeyError: "boom"},
	}

	details := failureDetails(result)

	r.Equal("boom", details[failureReasonDetailsKey])

	first, ok := details[firstResultDetailsKey].(models.JSONMap)
	r.True(ok, "first_result must be a models.JSONMap")
	r.Equal("result-1", first[snapshotKeyResultUID])
	r.Equal("DOWN", first[snapshotKeyStatus])
	r.Equal("eu", first[snapshotKeyRegion])
	r.InEpsilon(float32(1.5), first[snapshotKeyDuration], 0.0001)
	r.Equal(now, first[snapshotKeyPeriodStart])

	output, ok := first[snapshotKeyOutput].(models.JSONMap)
	r.True(ok)
	r.Equal("boom", output[checkerdef.OutputKeyError])

	// last_failure must be absent until a relapse happens.
	_, hasLastFailure := details[lastFailureDetailsKey]
	r.False(hasLastFailure)
}

func TestLastFailureDetails_PreservesExistingKeys(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	existing := models.JSONMap{
		failureReasonDetailsKey: "original reason",
		firstResultDetailsKey:   models.JSONMap{snapshotKeyResultUID: "first-result"},
	}

	status := int(models.ResultStatusTimeout)
	relapseResult := &models.Result{
		UID:    "relapse-result",
		Status: &status,
		Output: models.JSONMap{},
	}

	merged := lastFailureDetails(existing, relapseResult)

	r.Equal("original reason", merged[failureReasonDetailsKey],
		"reopen must not overwrite the original failure_reason")
	firstResult, ok := merged[firstResultDetailsKey].(models.JSONMap)
	r.True(ok)
	r.Equal("first-result", firstResult[snapshotKeyResultUID],
		"reopen must not overwrite the original first_result")

	lastFailure, ok := merged[lastFailureDetailsKey].(models.JSONMap)
	r.True(ok, "last_failure must be added")
	r.Equal("relapse-result", lastFailure[snapshotKeyResultUID])

	// The original map passed in must not be mutated (defensive copy).
	_, hadLastFailureBefore := existing[lastFailureDetailsKey]
	r.False(hadLastFailureBefore)
}

func TestLastFailureDetails_NilExisting(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	status := int(models.ResultStatusDown)
	result := &models.Result{UID: "r1", Status: &status, Output: models.JSONMap{}}

	merged := lastFailureDetails(nil, result)

	lastFailure, ok := merged[lastFailureDetailsKey].(models.JSONMap)
	r.True(ok)
	r.Equal("r1", lastFailure[snapshotKeyResultUID])
}

func TestCappedOutput_UnderCapPassesThrough(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	output := models.JSONMap{
		checkerdef.OutputKeyError:      "small error",
		checkerdef.OutputKeyStatusCode: 503,
	}

	capped := cappedOutput(output)
	r.Equal(output, capped)
}

func TestCappedOutput_EmptyOutput(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(models.JSONMap{}, cappedOutput(nil))
	r.Equal(models.JSONMap{}, cappedOutput(models.JSONMap{}))
}

// TestCappedOutput_OverCapKeepsErrorAndFitsBudget pins the size-cap contract:
// an output map that marshals over maxFailureSnapshotBytes must be shrunk to
// fit, and checkerdef.OutputKeyError must always survive, unmodified, in the
// result.
func TestCappedOutput_OverCapKeepsErrorAndFitsBudget(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	errMsg := "concise, load-bearing error message"
	output := models.JSONMap{
		checkerdef.OutputKeyError: errMsg,
		"huge_body":               strings.Repeat("x", 20*1024),
		"another_huge_field":      strings.Repeat("y", 20*1024),
		"small_field":             "kept-if-it-fits",
	}

	// Sanity: confirm the input really is over budget before capping.
	raw, err := json.Marshal(output)
	r.NoError(err)
	r.Greater(len(raw), maxFailureSnapshotBytes)

	capped := cappedOutput(output)

	cappedRaw, err := json.Marshal(capped)
	r.NoError(err)
	r.LessOrEqual(len(cappedRaw), maxFailureSnapshotBytes,
		"capped output must fit the size budget")

	r.Equal(errMsg, capped[checkerdef.OutputKeyError],
		"the error key must survive intact")
}

// TestCappedOutput_TruncatesOversizedStringValue pins the "truncate long
// strings" half of the cap: a single oversized string field is shortened
// rather than dropped outright, when truncation alone is enough to fit.
func TestCappedOutput_TruncatesOversizedStringValue(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	output := models.JSONMap{
		checkerdef.OutputKeyError: "boom",
		"body":                    strings.Repeat("a", 20*1024),
	}

	capped := cappedOutput(output)

	body, ok := capped["body"].(string)
	r.True(ok, "body key must survive (truncated), not be dropped, given only one oversized field")
	r.Less(len(body), 20*1024)
	r.Equal("boom", capped[checkerdef.OutputKeyError])
}

// TestCappedOutput_TruncatesHugeErrorValue pins the last-resort branch: when
// checkerdef.OutputKeyError alone is bigger than maxFailureSnapshotBytes, it
// must still be present (never dropped) but truncated to a prefix of the
// original, so the whole marshaled map respects the cap.
func TestCappedOutput_TruncatesHugeErrorValue(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	hugeError := strings.Repeat("e", 20*1024)
	output := models.JSONMap{
		checkerdef.OutputKeyError: hugeError,
	}

	capped := cappedOutput(output)

	errVal, ok := capped[checkerdef.OutputKeyError].(string)
	r.True(ok, "error key must still be present")
	r.NotEmpty(errVal)
	r.Less(len(errVal), len(hugeError), "the error value itself must have been shortened")

	// The retained text must be a genuine prefix of the original error
	// (modulo the trailing truncation marker), not something unrelated.
	prefix := strings.TrimSuffix(errVal, "…(truncated)")
	r.True(strings.HasPrefix(hugeError, prefix),
		"retained error text must be a prefix of the original")

	raw, err := json.Marshal(capped)
	r.NoError(err)
	r.LessOrEqual(len(raw), maxFailureSnapshotBytes,
		"the whole capped map — dominated by the error string — must respect the size cap")
}
