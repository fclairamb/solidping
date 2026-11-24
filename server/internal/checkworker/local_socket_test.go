package checkworker

import (
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// socketGateListener serves a Unix socket and counts the connections it
// accepted, so a test can prove a refused job never dialed.
func socketGateListener(t *testing.T) (string, *atomic.Int32) {
	t.Helper()

	dir, err := os.MkdirTemp("", "spw")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "app.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	accepted := new(atomic.Int32)

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			accepted.Add(1)
			_ = conn.Close()
		}
	}()

	return path, accepted
}

// newSocketGateRunner is newDockerGateRunner with this process's socket
// allowlist set to allow, running jobs that target path.
func newSocketGateRunner(
	t *testing.T, mode, role, path, allow string,
) (func() *models.Result, func() *models.Result) {
	t.Helper()

	_, dbSvc, ctx := setupTestRunner(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	runner := newDockerGateRunner(t, dbSvc, mode, role)
	runner.config.Checkers.UnixSocketPaths = []string{allow}

	runTCP := func() *models.Result {
		return runGateJob(t, ctx, runner, dbSvc, checkerdef.CheckTypeTCP, models.JSONMap{"socket": path})
	}
	runJS := func() *models.Result {
		return runGateJob(t, ctx, runner, dbSvc, checkerdef.CheckTypeJS, models.JSONMap{
			"script": `var r = solidping.check("tcp", { socket: "` + path + `" }); ` +
				`return { status: r.status, output: r.output };`,
		})
	}

	return runTCP, runJS
}

// TestExecuteJob_SocketTCPBlockedOnSaaSSharedWorker: a socket tcp job, and
// the same target as a js sub-check, are refused on a SaaS shared worker
// without dialing (spec 2026-11-24-01).
//
//nolint:paralleltest // the worker constructor installs the package-level checkjs gates
func TestExecuteJob_SocketTCPBlockedOnSaaSSharedWorker(t *testing.T) {
	r := require.New(t)
	path, accepted := socketGateListener(t)

	runTCP, runJS := newSocketGateRunner(t, config.DeploymentModeSaaS, "", path, path)

	result := runTCP()
	r.NotNil(result.Status)
	r.Equal(int(checkerdef.StatusError), *result.Status, "output: %v", result.Output)
	r.Equal(ErrHostLocalNotAvailableInSaaS.Error(), result.Output[checkerdef.OutputKeyError])

	result = runJS()
	r.NotNil(result.Status)
	r.Equal(int(checkerdef.StatusError), *result.Status, "output: %v", result.Output)
	r.Equal(ErrHostLocalNotAvailableInSaaS.Error(), result.Output[checkerdef.OutputKeyError])

	r.Zero(accepted.Load(), "a refused socket job must never dial")
}

// TestExecuteJob_SocketTCPRunsOnAgentWorker is the positive control: an
// agent runs the same job, and its sub-check, through its own allowlist.
//
//nolint:paralleltest // the worker constructor installs the package-level checkjs gates
func TestExecuteJob_SocketTCPRunsOnAgentWorker(t *testing.T) {
	r := require.New(t)
	path, accepted := socketGateListener(t)

	runTCP, runJS := newSocketGateRunner(t, config.DeploymentModeSaaS, config.NodeRoleAgent, path, path)

	result := runTCP()
	r.NotNil(result.Status)
	r.Equal(int(checkerdef.StatusUp), *result.Status, "output: %v", result.Output)

	result = runJS()
	r.NotNil(result.Status)
	r.Equal(int(checkerdef.StatusUp), *result.Status, "output: %v", result.Output)

	r.EqualValues(2, accepted.Load())
}

// TestExecuteJob_SocketTCPOutsideAllowlist: a self-hosted worker runs the job,
// but the dialer refuses a path its allowlist does not cover.
//
//nolint:paralleltest // the worker constructor installs the package-level checkjs gates
func TestExecuteJob_SocketTCPOutsideAllowlist(t *testing.T) {
	r := require.New(t)
	path, accepted := socketGateListener(t)

	runTCP, _ := newSocketGateRunner(t, config.DeploymentModeSelfHosted, "", path, "/nowhere/")

	result := runTCP()
	r.NotNil(result.Status)
	r.Equal(int(checkerdef.StatusError), *result.Status, "output: %v", result.Output)
	r.Contains(result.Output[checkerdef.OutputKeyError], checkerdef.LocalSocketSetting)
	r.Zero(accepted.Load())
}
