package models_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestStatusToString pins the UPPERCASE result-status serialization, including
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
		{models.CheckStatusCreated, models.WireStatusCreated},
		{models.CheckStatusUp, models.WireStatusUp},
		{models.CheckStatusDown, models.WireStatusDown},
		{models.CheckStatusValidating, models.WireStatusValidating},
		{models.CheckStatusDegraded, models.WireStatusDegraded},
		{models.CheckStatusWarning, models.WireStatusWarning},
		{models.CheckStatus(99), models.WireStatusUnknown},
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

	r.Equal(models.ResultStatusDegraded, models.ResultStatus(7))
	r.Equal(models.ResultStatusWarning, models.ResultStatus(8))
	r.Equal(models.CheckStatusDegraded, models.CheckStatus(7))
	r.Equal(models.CheckStatusWarning, models.CheckStatus(8))
}
