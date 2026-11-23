package checksystemd_test

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checksystemd"
)

var errNotBooted = errors.New("exit status 1: System has not been booted with systemd")

func TestExecuteArgv(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for unit, want := range map[string]string{
		"nginx":         "nginx.service",
		"app@1.service": "app@1.service",
		"foo.timer":     "foo.timer",
	} {
		fake := &fakeRunner{out: showInactive}
		result := run(t, t.Context(), fake.run, map[string]any{"unit": unit})

		r.Equal([][]string{{
			"systemctl", "show",
			"--property=LoadState,ActiveState,SubState,Result,ActiveEnterTimestamp",
			"--no-pager", "--", want,
		}}, fake.calls)
		r.Equal(want, result.Output[checksystemd.OutputKeyUnit])
	}
}

func TestExecuteGradesOutput(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake := &fakeRunner{out: showFailed}
	result := run(t, t.Context(), fake.run, map[string]any{"unit": "nginx"})
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal("failed/failed", result.Output[checksystemd.OutputKeyState])

	result = run(t, t.Context(), fake.run, map[string]any{"unit": "nginx", "expected_state": "inactive"})
	r.Equal(checkerdef.StatusUp, result.Status)
}

func TestExecuteMissingSystemctlIsError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake := &fakeRunner{err: &exec.Error{Name: "systemctl", Err: exec.ErrNotFound}}
	result := run(t, t.Context(), fake.run, map[string]any{"unit": "nginx"})

	r.Equal(checkerdef.StatusError, result.Status)
	r.Equal(checksystemd.ErrSystemctlMissing.Error(), result.Output[checkerdef.OutputKeyError])
}

func TestExecuteSystemctlFailureIsError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake := &fakeRunner{err: errNotBooted}
	result := run(t, t.Context(), fake.run, map[string]any{"unit": "nginx"})

	r.Equal(checkerdef.StatusError, result.Status)
	r.Contains(result.Output[checkerdef.OutputKeyError], "not been booted with systemd")
}

func TestExecuteTimeout(t *testing.T) {
	t.Parallel()

	blocking := func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()

		return nil, ctx.Err()
	}

	result := run(t, t.Context(), blocking, map[string]any{"unit": "nginx", "timeout": "50ms"})
	require.Equal(t, checkerdef.StatusTimeout, result.Status)
}

// The default runner really execs systemctl: on a host without it (macOS,
// a minimal container) that must be an error, not down.
func TestExecuteDefaultRunnerWithoutSystemctl(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("systemctl"); err == nil {
		t.Skip("systemctl is installed")
	}

	cfg := &checksystemd.SystemdConfig{Unit: "nginx"}
	result, err := (&checksystemd.SystemdChecker{}).Execute(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, checkerdef.StatusError, result.Status)
	require.Equal(t, checksystemd.ErrSystemctlMissing.Error(), result.Output[checkerdef.OutputKeyError])
}

// A config that skipped validation (a stale row) never reaches systemctl.
func TestExecuteRevalidatesUnit(t *testing.T) {
	t.Parallel()

	fake := &fakeRunner{out: showInactive}
	result, err := (&checksystemd.SystemdChecker{Run: fake.run}).Execute(
		t.Context(), &checksystemd.SystemdConfig{Unit: "--host=evil"})
	require.NoError(t, err)
	require.Equal(t, checkerdef.StatusError, result.Status)
	require.Zero(t, fake.callCount())
}
