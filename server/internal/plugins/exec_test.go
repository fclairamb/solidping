//go:build !windows

package plugins

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func testConfig(values map[string]any) *Config {
	cfg := &Config{}
	_ = cfg.FromMap(values)

	return cfg
}

func newHelperExec(t *testing.T, mode string, extraEnv ...string) *execTransport {
	t.Helper()

	dir := t.TempDir()
	tr := newExecTransport(os.Args[0], dir, append(helperEnvFor(dir, mode), extraEnv...), slog.Default())
	t.Cleanup(tr.close)

	return tr
}

func TestExecRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	tr := newHelperExec(t, "exec")

	res, err := tr.execute(t.Context(), "x-test", testConfig(map[string]any{"host": "gopher.acme.com"}))
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, res.Status, res.Output)
	r.Equal("hello gopher.acme.com", res.Output["message"])
	r.Equal("x-test", res.Output["type"])

	// Same long-lived process for the second call.
	again, err := tr.execute(t.Context(), "x-test", testConfig(map[string]any{"host": "b"}))
	r.NoError(err)
	r.Equal(res.Metrics["pid"], again.Metrics["pid"])

	r.NoError(tr.validate(t.Context(), "x-test", map[string]any{"host": "ok.acme.com"}))

	verr := tr.validate(t.Context(), "x-test", map[string]any{"host": "forbidden.acme.com"})
	r.Error(verr)
	r.NotNil(checkerdef.IsConfigError(verr))
	r.Contains(verr.Error(), "not allowed")
}

func TestExecCrashThenRestart(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	marker := filepath.Join(t.TempDir(), "marker")
	tr := newHelperExec(t, "exec-crash-once", "SP_PLUGIN_TEST_MARKER="+marker)

	res, err := tr.execute(t.Context(), "x-test", testConfig(map[string]any{"host": "a"}))
	r.NoError(err)
	r.Equal(checkerdef.StatusError, res.Status)
	r.Contains(fmt.Sprint(res.Output[checkerdef.OutputKeyError]), "exited")

	// After the backoff the next call starts a fresh process that answers.
	r.Eventually(func() bool {
		res, err = tr.execute(t.Context(), "x-test", testConfig(map[string]any{"host": "a"}))

		return err == nil && res.Status == checkerdef.StatusUp
	}, 10*time.Second, 50*time.Millisecond)
}

func TestExecDeadlineIsATimeout(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	tr := newHelperExec(t, "exec-slow")

	start := time.Now()
	res, err := tr.execute(t.Context(), "x-test", testConfig(map[string]any{"timeout": "300ms"}))
	r.NoError(err)
	r.Equal(checkerdef.StatusTimeout, res.Status, res.Output)
	r.Less(time.Since(start), 5*time.Second)
}

// A plugin that stops reading its stdin cannot block a call past the check
// deadline: the request is bigger than the pipe buffer, so the write itself
// would block until the process exits.
func TestExecPluginNotReadingStdinIsATimeout(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	tr := newHelperExec(t, "sleep")

	start := time.Now()
	res, err := tr.execute(t.Context(), "x-test", testConfig(map[string]any{
		"timeout": "300ms", "blob": strings.Repeat("x", 4*1024*1024),
	}))
	r.NoError(err)
	r.Equal(checkerdef.StatusTimeout, res.Status, res.Output)
	r.Less(time.Since(start), 5*time.Second)
}

func TestNagiosExitCodes(t *testing.T) {
	t.Parallel()

	cases := map[int]checkerdef.Status{
		0: checkerdef.StatusUp,
		1: checkerdef.StatusWarning,
		2: checkerdef.StatusDown,
		3: checkerdef.StatusError,
		7: checkerdef.StatusError,
	}

	for code, want := range cases {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			dir := t.TempDir()
			tr := newNagiosTransport(os.Args[0], dir, []string{"-H", "{{host}}", "--opt={{missing}}", "{{missing}}"},
				helperEnvFor(dir, "nagios-exit-"+strconv.Itoa(code)))

			res, err := tr.execute(t.Context(), "x-test", testConfig(map[string]any{"host": "gopher.acme.com"}))
			r.NoError(err)
			r.Equal(want, res.Status, res.Output)
			r.Equal(fmt.Sprintf("STATUS %d - args=-H,gopher.acme.com,--opt=", code), res.Output["message"])
			r.InDelta(0.25, res.Metrics["time"], 1e-9)
			r.InDelta(42, res.Metrics["disk used"], 1e-9)
			r.InDelta(7, res.Metrics["extra"], 1e-9)
			r.NotContains(res.Metrics, "bad", "malformed perfdata is ignored")
		})
	}
}

func TestParsePerfdata(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	message, metrics := parseNagiosOutput(
		"OK - fine | a=1 b=2.5ms;3;4;0;10 c=10% 'my label'=3 broken =4 d= e=x f=-1.5KB\n")
	r.Equal("OK - fine", message)
	r.Equal(map[string]any{"a": 1.0, "b": 2.5, "c": 10.0, "my label": 3.0, "f": -1.5}, metrics)

	message, metrics = parseNagiosOutput("no perfdata here")
	r.Equal("no perfdata here", message)
	r.Empty(metrics)
}

func TestNagiosTimeoutKillsChildren(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "child.pid")
	tr := newNagiosTransport(os.Args[0], dir, nil,
		append(helperEnvFor(dir, "nagios-hang"), "SP_PLUGIN_TEST_MARKER="+marker))

	start := time.Now()
	res, err := tr.execute(t.Context(), "x-test", testConfig(map[string]any{"timeout": "2s"}))
	r.NoError(err)
	r.Equal(checkerdef.StatusTimeout, res.Status, res.Output)
	r.Less(time.Since(start), 10*time.Second)

	// Positive control: the plugin did fork a child before it hung.
	raw, err := os.ReadFile(marker)
	r.NoError(err)

	childPID, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	r.NoError(err)
	r.Positive(childPID)

	r.Eventually(func() bool {
		return syscall.Kill(childPID, 0) != nil
	}, 5*time.Second, 50*time.Millisecond, "child process survived the timeout")
}
