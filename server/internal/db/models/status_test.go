package models_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestStatusToString pins the UPPERCASE result-status serialisation, including
// the new WARNING (raw) and DEGRADED (aggregated) values.
func TestStatusToString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status models.ResultStatus
		want   string
	}{
		{models.ResultStatusCreated, "CREATED"},
		{models.ResultStatusRunning, "RUNNING"},
		{models.ResultStatusUp, "UP"},
		{models.ResultStatusDown, "DOWN"},
		{models.ResultStatusTimeout, "TIMEOUT"},
		{models.ResultStatusError, "ERROR"},
		{models.ResultStatusDegraded, "DEGRADED"},
		{models.ResultStatusWarning, "WARNING"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, models.StatusToString(int(tc.status)))
		})
	}

	require.Equal(t, "UNKNOWN", models.StatusToString(99))
}

// TestCheckStatusString pins the lowercase current-status wire names, including
// the new "warning" (live) and the retained "degraded" (summary).
func TestCheckStatusString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status models.CheckStatus
		want   string
	}{
		{models.CheckStatusCreated, "created"},
		{models.CheckStatusUp, "up"},
		{models.CheckStatusDown, "down"},
		{models.CheckStatusValidating, "validating"},
		{models.CheckStatusDegraded, "degraded"},
		{models.CheckStatusWarning, "warning"},
		{models.CheckStatus(99), "unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.status.String())
		})
	}
}

// TestStatusEnumValues pins the numeric values shared with checkerdef and the
// DB constraint (two downstream specs depend on Warning=8).
func TestStatusEnumValues(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(models.ResultStatus(7), models.ResultStatusDegraded)
	r.Equal(models.ResultStatus(8), models.ResultStatusWarning)
	r.Equal(models.CheckStatus(7), models.CheckStatusDegraded)
	r.Equal(models.CheckStatus(8), models.CheckStatusWarning)
}
