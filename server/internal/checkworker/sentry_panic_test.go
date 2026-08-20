package checkworker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/errorreport/errorreporttest"
)

// targetDownChecker is the control: a perfectly healthy checker reporting that
// the thing it monitors is down. That is the product working as designed and
// must never reach Sentry — if it did, every real outage would page us twice
// and bury the crashes this reporting exists to surface.
type targetDownChecker struct{}

func (targetDownChecker) Type() checkerdef.CheckType           { return checkerdef.CheckType("test-down") }
func (targetDownChecker) Validate(*checkerdef.CheckSpec) error { return nil }

func (targetDownChecker) Execute(context.Context, checkerdef.Config) (*checkerdef.Result, error) {
	return &checkerdef.Result{
		Status: checkerdef.StatusDown,
		Output: map[string]any{"error": "connection refused"},
	}, nil
}

func TestRunCheckerGuarded_PanicIsReportedWithCheckTags(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	runner, dbSvc, ctx := setupTestRunner(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	hub, transport, err := errorreporttest.NewHub()
	r.NoError(err)

	ctx = sentry.SetHubOnContext(ctx, hub)

	checkJob := testCheckJob("test-panicking")

	_, execErr := runner.runCheckerGuarded(
		ctx, runner.logger, panickingChecker{}, hangingConfig{}, checkJob, time.Second, time.Now(),
	)
	r.ErrorIs(execErr, ErrCheckerPanic)

	events := transport.Events()
	r.Len(events, 1)
	r.Equal("test-panicking", events[0].Tags["check.type"])
	r.Equal(checkJob.CheckUID, events[0].Tags["check.uid"])
	r.Equal(checkJob.OrganizationUID, events[0].Tags["organization"])
	r.Contains(exceptionText(events[0]), "simulated checker panic")
}

// exceptionText joins every exception in the event's chain: sentry-go unwraps
// the %w chain into one entry per wrapping level, so the panic value sits at
// the far end of it, not at index 0.
func exceptionText(event *sentry.Event) string {
	parts := make([]string, 0, len(event.Exception)+1)
	parts = append(parts, event.Message)

	for _, exception := range event.Exception {
		parts = append(parts, exception.Value)
	}

	return strings.Join(parts, " | ")
}

// Positive control for the test above: same code path, same hub, a checker that
// reports its target down instead of crashing. Zero events.
func TestRunCheckerGuarded_TargetDownIsNotReported(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	runner, dbSvc, ctx := setupTestRunner(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	hub, transport, err := errorreporttest.NewHub()
	r.NoError(err)

	ctx = sentry.SetHubOnContext(ctx, hub)

	result, execErr := runner.runCheckerGuarded(
		ctx, runner.logger, targetDownChecker{}, hangingConfig{}, testCheckJob("test-down"),
		time.Second, time.Now(),
	)
	r.NoError(execErr)
	r.NotNil(result)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal(0, transport.Len(), "a check whose target is down is normal operation, not an error to report")
}
