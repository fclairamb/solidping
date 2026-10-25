package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/plugins/protocol"
)

// Exec transport tuning.
const (
	execMaxLine        = 4 * 1024 * 1024 // longest JSON-RPC line read from a plugin
	execBackoffInitial = 200 * time.Millisecond
	execBackoffMax     = 30 * time.Second
)

// Exec transport errors.
var (
	ErrPluginExited     = errors.New("plugin process exited")
	ErrPluginRestarting = errors.New("plugin process is restarting after a crash")
	ErrPluginClosed     = errors.New("plugin is shut down")
	ErrPluginRPC        = errors.New("plugin answered with an error")
)

// baseEnv is the environment a plugin process gets: never the server's own
// (which holds the encryption key and database credentials).
func baseEnv(dir string) []string {
	env := []string{"SOLIDPING_PLUGIN_DIR=" + dir}

	for _, key := range []string{"PATH", "HOME", "LANG", "TZ", "TMPDIR", "SYSTEMROOT"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}

	return env
}

// execTransport keeps one long-lived plugin process and talks JSON-RPC 2.0 to
// it over stdin/stdout. A crashed process is restarted on the next call, with
// exponential backoff; calls in flight when it died fail.
type execTransport struct {
	path   string
	dir    string
	env    []string
	logger *slog.Logger

	nextID atomic.Int64

	mu         sync.Mutex
	proc       *execProcess
	closed     bool
	backoff    time.Duration
	retryAfter time.Time
}

type execProcess struct {
	cmd *exec.Cmd
	// stdin is our own pipe (not cmd.StdinPipe) so a write can carry the
	// request's deadline: a plugin that stops reading must not block a call
	// past it.
	stdin *os.File
	// writeSlot serializes writes; a channel so waiting for it honors the
	// request's context.
	writeSlot chan struct{}

	pendingMu sync.Mutex
	pending   map[int64]chan protocol.Response

	done chan struct{}
}

func newExecTransport(path, dir string, env []string, logger *slog.Logger) *execTransport {
	return &execTransport{path: path, dir: dir, env: env, logger: logger}
}

// process returns the running process, starting one when needed.
func (t *execTransport) process() (*execProcess, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil, ErrPluginClosed
	}

	if t.proc != nil {
		select {
		case <-t.proc.done:
			t.proc = nil
		default:
			return t.proc, nil
		}
	}

	if time.Now().Before(t.retryAfter) {
		return nil, ErrPluginRestarting
	}

	proc, err := t.start()
	if err != nil {
		t.crashed()

		return nil, err
	}

	t.proc = proc

	return proc, nil
}

// crashed schedules the next restart. Caller holds t.mu.
func (t *execTransport) crashed() {
	if t.backoff == 0 {
		t.backoff = execBackoffInitial
	} else {
		t.backoff = min(t.backoff*2, execBackoffMax)
	}

	t.retryAfter = time.Now().Add(t.backoff)
}

func (t *execTransport) start() (*execProcess, error) {
	// The path comes from the manifest, resolved inside the plugin folder.
	// No context: the process outlives any one request.
	//nolint:noctx // long-lived process
	cmd := exec.Command(t.path)
	cmd.Dir = t.dir
	cmd.Env = t.env
	setProcessGroup(cmd)

	stdinRead, stdin, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("plugin stdin: %w", err)
	}

	cmd.Stdin = stdinRead

	proc, err := t.startWith(cmd, stdin)

	// The child holds its own copy of the read end.
	_ = stdinRead.Close()

	if err != nil {
		_ = stdin.Close()

		return nil, err
	}

	return proc, nil
}

func (t *execTransport) startWith(cmd *exec.Cmd, stdin *os.File) (*execProcess, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin stdout: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting plugin %s: %w", t.path, err)
	}

	proc := &execProcess{
		cmd:       cmd,
		stdin:     stdin,
		writeSlot: make(chan struct{}, 1),
		pending:   map[int64]chan protocol.Response{},
		done:      make(chan struct{}),
	}

	go t.logStderr(stderr)
	go t.readLoop(proc, stdout)

	return proc, nil
}

func (t *execTransport) logStderr(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		t.logger.Info("plugin stderr", "plugin", t.path, "line", scanner.Text())
	}
}

func (t *execTransport) readLoop(proc *execProcess, stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), execMaxLine)

	for scanner.Scan() {
		var resp protocol.Response
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			t.logger.Warn("plugin wrote a line that is not a JSON-RPC response", "plugin", t.path, "error", err)

			continue
		}

		proc.pendingMu.Lock()
		waiter, ok := proc.pending[resp.ID]
		delete(proc.pending, resp.ID)
		proc.pendingMu.Unlock()

		if ok {
			waiter <- resp
		}
	}

	_ = proc.stdin.Close()
	waitErr := proc.cmd.Wait()

	t.mu.Lock()
	if !t.closed {
		t.crashed()
		t.logger.Warn("plugin process exited", "plugin", t.path, "error", waitErr)
	}
	t.mu.Unlock()

	close(proc.done)
}

// call sends one request and waits for its response, the context or the
// process exit, whichever comes first.
func (t *execTransport) call(ctx context.Context, method string, params, out any) error {
	proc, err := t.process()
	if err != nil {
		return err
	}

	id := t.nextID.Add(1)
	waiter := make(chan protocol.Response, 1)

	proc.pendingMu.Lock()
	proc.pending[id] = waiter
	proc.pendingMu.Unlock()

	defer func() {
		proc.pendingMu.Lock()
		delete(proc.pending, id)
		proc.pendingMu.Unlock()
	}()

	line, err := json.Marshal(protocol.Request{JSONRPC: protocol.JSONRPCVersion, ID: id, Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("encoding plugin request: %w", err)
	}

	if err := proc.write(ctx, append(line, '\n')); err != nil {
		return err
	}

	select {
	case resp := <-waiter:
		t.mu.Lock()
		t.backoff = 0
		t.mu.Unlock()

		if resp.Error != nil {
			return fmt.Errorf("%w: %d %s", ErrPluginRPC, resp.Error.Code, resp.Error.Message)
		}

		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("%w: undecodable result: %w", ErrPluginRPC, err)
		}

		return nil
	case <-proc.done:
		return ErrPluginExited
	case <-ctx.Done():
		return ctx.Err() //nolint:wrapcheck // the caller tests for DeadlineExceeded
	}
}

// write sends one request line, bounded by ctx: waiting for the write slot
// and the write itself both stop at its deadline. A write cut short leaves a
// partial line on the pipe, so the process is killed and restarts on the
// next call.
func (proc *execProcess) write(ctx context.Context, line []byte) error {
	select {
	case proc.writeSlot <- struct{}{}:
	case <-proc.done:
		return ErrPluginExited
	case <-ctx.Done():
		return ctx.Err() //nolint:wrapcheck // the caller tests for DeadlineExceeded
	}

	deadline, _ := ctx.Deadline()
	// Zero clears it. A pipe without deadline support (Windows) writes
	// unbounded, as before.
	_ = proc.stdin.SetWriteDeadline(deadline)

	_, err := proc.stdin.Write(line)

	<-proc.writeSlot

	switch {
	case err == nil:
		return nil
	case errors.Is(err, os.ErrDeadlineExceeded):
		_ = killProcessGroup(proc.cmd)

		return fmt.Errorf("plugin stopped reading its requests: %w", context.DeadlineExceeded)
	default:
		return fmt.Errorf("%w: %w", ErrPluginExited, err)
	}
}

func (t *execTransport) execute(ctx context.Context, checkType string, cfg *Config) (*checkerdef.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout())
	defer cancel()

	deadline, _ := ctx.Deadline()
	start := time.Now()

	var res protocol.ExecuteResult

	err := t.call(ctx, protocol.MethodExecute, protocol.ExecuteParams{
		Type: checkType, Config: cfg.GetConfig(), Deadline: deadline,
	}, &res)

	duration := time.Since(start)

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &checkerdef.Result{
			Status: checkerdef.StatusTimeout, Duration: duration,
			Output: map[string]any{checkerdef.OutputKeyError: "plugin did not answer before the deadline"},
		}, nil
	case err != nil:
		return &checkerdef.Result{
			Status: checkerdef.StatusError, Duration: duration,
			Output: map[string]any{checkerdef.OutputKeyError: err.Error()},
		}, nil
	}

	return resultFromProtocol(&res, duration), nil
}

// validate asks the plugin to validate a config the schema accepted.
func (t *execTransport) validate(ctx context.Context, checkType string, config map[string]any) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var res protocol.ValidateResult

	params := protocol.ValidateParams{Type: checkType, Config: config}
	if err := t.call(ctx, protocol.MethodValidate, params, &res); err != nil {
		return checkerdef.NewConfigError("config", "plugin could not validate the config: "+err.Error())
	}

	if len(res.Errors) > 0 {
		return checkerdef.NewConfigError(res.Errors[0].Field, res.Errors[0].Message)
	}

	return nil
}

func (t *execTransport) close() {
	t.mu.Lock()
	t.closed = true
	proc := t.proc
	t.proc = nil
	t.mu.Unlock()

	if proc != nil {
		_ = proc.stdin.Close()
		_ = killProcessGroup(proc.cmd)
		<-proc.done
	}
}

// resultFromProtocol maps a plugin's answer to a check result. An unknown
// status becomes an error that names it.
func resultFromProtocol(res *protocol.ExecuteResult, duration time.Duration) *checkerdef.Result {
	output := res.Output
	if output == nil {
		output = map[string]any{}
	}

	status, ok := checkerdef.ParseStatus(res.Status)
	if !ok {
		status = checkerdef.StatusError
		output[checkerdef.OutputKeyError] = fmt.Sprintf("plugin returned an unknown status %q", res.Status)
	}

	return &checkerdef.Result{Status: status, Duration: duration, Output: output, Metrics: res.Metrics}
}
