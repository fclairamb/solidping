package checkerdef_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// TestStatusString pins the wire strings, including the new "warning".
func TestStatusString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status checkerdef.Status
		want   string
	}{
		{checkerdef.StatusRunning, "running"},
		{checkerdef.StatusUp, "up"},
		{checkerdef.StatusDown, "down"},
		{checkerdef.StatusTimeout, "timeout"},
		{checkerdef.StatusError, "error"},
		{checkerdef.StatusWarning, "warning"},
		{checkerdef.Status(99), "unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.status.String())
		})
	}
}

// TestStatusSeverity pins the gravity ordering the aggregation job relies on:
// failures > Degraded > Up, with Warning ranked at Up level.
func TestStatusSeverity(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Greater(checkerdef.StatusDown.Severity(), checkerdef.StatusDegraded.Severity())
	r.Greater(checkerdef.StatusTimeout.Severity(), checkerdef.StatusDegraded.Severity())
	r.Greater(checkerdef.StatusError.Severity(), checkerdef.StatusDegraded.Severity())
	r.Greater(checkerdef.StatusDegraded.Severity(), checkerdef.StatusUp.Severity())
	r.Equal(checkerdef.StatusUp.Severity(), checkerdef.StatusWarning.Severity())

	// All hard failures share the top rank.
	r.Equal(checkerdef.StatusDown.Severity(), checkerdef.StatusTimeout.Severity())
	r.Equal(checkerdef.StatusDown.Severity(), checkerdef.StatusError.Severity())
}

// TestStatusEnumValues pins the numeric values two downstream specs depend on.
func TestStatusEnumValues(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(checkerdef.StatusDegraded, checkerdef.Status(7))
	r.Equal(checkerdef.StatusWarning, checkerdef.Status(8))
}
