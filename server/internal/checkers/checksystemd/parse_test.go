package checksystemd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checksystemd"
)

const (
	showActiveRunning = "LoadState=loaded\nActiveState=active\nSubState=running\nResult=success\n" +
		"ActiveEnterTimestamp=%s\n"
	showActivating = "LoadState=loaded\nActiveState=activating\nSubState=auto-restart\nResult=exit-code\n" +
		"ActiveEnterTimestamp=\n"
	showFailed = "LoadState=loaded\nActiveState=failed\nSubState=failed\nResult=exit-code\n" +
		"ActiveEnterTimestamp=\n"
	showInactive = "LoadState=loaded\nActiveState=inactive\nSubState=dead\nResult=success\n" +
		"ActiveEnterTimestamp=n/a\n"
	showNotFound = "LoadState=not-found\nActiveState=inactive\nSubState=dead\nResult=success\n" +
		"ActiveEnterTimestamp=\n"
)

func TestGrade(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	enteredAt := now.Add(-90 * time.Second).Format("Mon 2006-01-02 15:04:05 MST")
	activeRunning := sprintf(showActiveRunning, enteredAt)

	for _, tc := range []struct {
		name       string
		out        string
		wantActive bool
		status     checkerdef.Status
		state      string
		result     string
		hasError   bool
	}{
		{"active/running", activeRunning, true, checkerdef.StatusUp, "active/running", "success", false},
		{"activating/auto-restart", showActivating, true, checkerdef.StatusUp, "activating/auto-restart", "exit-code", false},
		{"failed/failed", showFailed, true, checkerdef.StatusDown, "failed/failed", "exit-code", true},
		{"inactive/dead", showInactive, true, checkerdef.StatusDown, "inactive/dead", "success", true},
		{"not-found", showNotFound, true, checkerdef.StatusDown, "", "", true},
		{"inverted active/running", activeRunning, false, checkerdef.StatusDown, "active/running", "success", true},
		{"inverted activating", showActivating, false, checkerdef.StatusDown, "activating/auto-restart", "exit-code", true},
		{"inverted failed/failed", showFailed, false, checkerdef.StatusUp, "failed/failed", "exit-code", false},
		{"inverted inactive/dead", showInactive, false, checkerdef.StatusUp, "inactive/dead", "success", false},
		{"inverted not-found", showNotFound, false, checkerdef.StatusDown, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			result := checksystemd.Grade(checksystemd.ParseShow([]byte(tc.out)), tc.wantActive, now)
			r.Equal(tc.status, result.Status)

			if tc.state != "" {
				r.Equal(tc.state, result.Output[checksystemd.OutputKeyState])
				r.Equal(tc.result, result.Output[checksystemd.OutputKeyResult])
			} else {
				r.Equal("unit not found", result.Output[checkerdef.OutputKeyError])
			}

			_, hasError := result.Output[checkerdef.OutputKeyError]
			r.Equal(tc.hasError, hasError, "output: %v", result.Output)
		})
	}
}

func TestGradeActiveSince(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	enteredAt := now.Add(-90 * time.Second).Format("Mon 2006-01-02 15:04:05 MST")

	result := checksystemd.Grade(checksystemd.ParseShow([]byte(sprintf(showActiveRunning, enteredAt))), true, now)
	r.InDelta(90.0, result.Metrics["active_since_s"], 0.001)

	result = checksystemd.Grade(checksystemd.ParseShow([]byte(showActivating)), true, now)
	r.NotContains(result.Metrics, "active_since_s")
}

func TestGradeEmptyOutputIsError(t *testing.T) {
	t.Parallel()

	result := checksystemd.Grade(checksystemd.ParseShow(nil), true, time.Now())
	require.Equal(t, checkerdef.StatusError, result.Status)
}
