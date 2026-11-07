package checkjs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/dop251/goja"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/egress"
)

// A js script runs in a child process of the same binary (spec 2026-11-02-06):
// goja checks its interrupt only between instructions, so a native call that
// allocates a few GB in one go would take the shared check worker (or the API
// process, for the AI runner) down with it. The child has a hard memory limit
// and is killed at the timeout; the bindings that need this process (the
// egress guard and allowlists, sub-checks, the browser, RDP and VNC pools,
// sockets) are served here, over the child's stdin and stdout, so their
// rules stay enforced in one place. The in-process memory budget stays the
// first line, in the child: it gives the clear `memory budget exceeded`
// message in the common cases.

const (
	// childEnvVar marks the process as a script child. It is the hidden
	// subcommand: any binary linking this package becomes a child when it is
	// set (see the init in isolation_child.go).
	childEnvVar = "SP_JS_CHILD"
	// childExitMemory is the child's exit code when its memory watchdog
	// fired.
	childExitMemory = 3
	// defaultChildMemoryLimit is the child's heap limit.
	defaultChildMemoryLimit = 256 << 20
	// minChildMemoryLimit is the smallest limit SP_JS_MEMORY_LIMIT_MB may set.
	minChildMemoryLimit = 32 << 20
	// childKillGrace is how long past the timeout a child may take to
	// report before it is killed: the interrupt lands at the next
	// instruction, so only a stuck native call needs it.
	childKillGrace = time.Second
	// childStderrCap bounds what is kept of the child's stderr.
	childStderrCap = 16 << 10
	// encodeMaxNodes bounds the nodes a served value is walked into.
	encodeMaxNodes = 1 << 20
	// encodeMaxDepth bounds how deep a served value is walked.
	encodeMaxDepth = 64
	// bytesPerMiB converts SP_JS_MEMORY_LIMIT_MB.
	bytesPerMiB = 1 << 20
)

// goErrorName is the name of the error goja wraps a Go error in.
const goErrorName = "GoError"

// Errors of the channel.
var (
	errChildProtocol = errors.New("unexpected message from the script process")
	errUnknownHandle = errors.New("unknown binding handle")
	errValueTooLarge = errors.New("value too large or too deep to cross to the script process")
	errNoChildBinary = errors.New("the script process binary is unknown")
)

// IsolationSettings decides how scripts run.
type IsolationSettings struct {
	// Enabled runs each script in a child process. Off runs it in this
	// process, as before the spec (SP_JS_ISOLATION=off).
	Enabled bool
	// MemoryLimit is the child's memory limit, in bytes
	// (SP_JS_MEMORY_LIMIT_MB).
	MemoryLimit int64
	// CgroupDir is a delegated cgroup v2 directory each child gets its own
	// sub-cgroup of, with memory.max set (SP_JS_CGROUP). Linux only; empty
	// means none.
	CgroupDir string
	// Executable is the binary started as the child; empty means this one.
	Executable string
}

//nolint:gochecknoglobals // Process-wide setting, read on every run.
var isolationSettings atomic.Pointer[IsolationSettings]

//nolint:gochecknoinits // Reads the operator's environment once.
func init() {
	settings := isolationFromEnv(os.Getenv)
	isolationSettings.Store(&settings)
}

// isolationFromEnv reads the settings from the environment.
func isolationFromEnv(getenv func(string) string) IsolationSettings {
	settings := IsolationSettings{Enabled: true, MemoryLimit: defaultChildMemoryLimit}

	switch strings.ToLower(strings.TrimSpace(getenv("SP_JS_ISOLATION"))) {
	case "off", "false", "0", "no":
		settings.Enabled = false
	}

	if raw := strings.TrimSpace(getenv("SP_JS_MEMORY_LIMIT_MB")); raw != "" {
		if mib, err := strconv.ParseInt(raw, 10, 64); err == nil && mib*bytesPerMiB >= minChildMemoryLimit {
			settings.MemoryLimit = mib * bytesPerMiB
		}
	}

	settings.CgroupDir = strings.TrimSpace(getenv("SP_JS_CGROUP"))

	return settings
}

// Isolation returns the settings scripts run with.
func Isolation() IsolationSettings {
	return currentIsolation()
}

// SetIsolation replaces the settings and returns the previous ones.
func SetIsolation(settings IsolationSettings) IsolationSettings {
	if settings.MemoryLimit <= 0 {
		settings.MemoryLimit = defaultChildMemoryLimit
	}

	return *isolationSettings.Swap(&settings)
}

func currentIsolation() IsolationSettings {
	return *isolationSettings.Load()
}

// handleEntry is a function the parent serves, with the object it was read
// from as `this`.
type handleEntry struct {
	fn   goja.Callable
	this goja.Value
}

// hostChannel serves the child's calls on the parent's bindings.
type hostChannel struct {
	runtime *jsRuntime
	handles []handleEntry
	nodes   int
}

// executeIsolated runs the script in a child process. The host runtime
// holds the bindings, and everything they hold (pages, sessions, sockets) is
// released here whatever the child did.
func executeIsolated(
	ctx context.Context, cfg *JSConfig, timeout time.Duration, repairList *egress.HostAllowlist,
	settings IsolationSettings,
) *checkerdef.Result {
	start := time.Now()

	host := newJSRuntime(ctx, cfg)

	defer host.closeBrowser()
	defer host.closeRDP()
	defer host.closeVNC()
	defer host.closeSockets()

	//nolint:contextcheck // bindings derive from r.execCtx by design
	host.registerGlobals()

	channel := &hostChannel{runtime: host}

	globals := make(map[string]*wireNode, len(hostGlobals))

	for _, name := range hostGlobals {
		node, err := channel.encode(host.vm.Get(name), 0)
		if err != nil {
			return host.isolationError(start, "could not expose "+name+": "+err.Error())
		}

		globals[name] = node
	}

	deadline, _ := ctx.Deadline()

	setup := &wireInit{
		Script:       cfg.Script,
		Env:          cfg.Env,
		Secrets:      cfg.Secrets,
		PluginConfig: cfg.PluginConfig,
		Plugin:       cfg.PluginConfig != nil,
		TimeoutNs:    int64(timeout),
		DeadlineNs:   deadline.UnixNano(),
		MemoryLimit:  settings.MemoryLimit,
		Globals:      globals,
	}

	result := channel.run(ctx, setup, settings, start, timeout)

	// The child's console, for the results built here.
	if console, ok := result.Output["console"].(string); ok && host.consoleBuf.Len() == 0 {
		host.consoleBuf.WriteString(console)
	}

	host.attachScreenshot(result)

	if refused := repairList.Refused(); refused != nil {
		return host.repairRefusal(refused, result.Duration)
	}

	return result
}

// isolationError is the result of a run the child could not complete.
func (r *jsRuntime) isolationError(start time.Time, msg string) *checkerdef.Result {
	return &checkerdef.Result{
		Status:   checkerdef.StatusError,
		Duration: time.Since(start),
		Output:   r.buildOutput("script error: " + msg),
	}
}

// cappedBuffer keeps the first cap bytes written to it.
type cappedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	cap int
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if room := b.cap - b.buf.Len(); room > 0 {
		if len(data) > room {
			b.buf.Write(data[:room])
		} else {
			b.buf.Write(data)
		}
	}

	return len(data), nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// childOutcome is how the child's run ended, as the parent saw it.
type childOutcome struct {
	done     *wireResult
	protoErr error
	waitErr  error
	stderr   string
	killedAt bool // killed by the parent at the timeout
	canceled bool // the run's context was canceled
}

// run starts the child, serves its calls until it reports, and turns how it
// ended into a result.
func (c *hostChannel) run(
	ctx context.Context, init *wireInit, settings IsolationSettings, start time.Time, timeout time.Duration,
) *checkerdef.Result {
	outcome, err := c.runChild(ctx, init, settings, timeout)
	if err != nil {
		return c.runtime.isolationError(start, "could not start the script process: "+err.Error())
	}

	return c.runtime.outcomeResult(outcome, settings, start, timeout)
}

// runChild runs one child to its end.
func (c *hostChannel) runChild(
	ctx context.Context, init *wireInit, settings IsolationSettings, timeout time.Duration,
) (*childOutcome, error) {
	exe := settings.Executable
	if exe == "" {
		var err error

		if exe, err = os.Executable(); err != nil {
			return nil, fmt.Errorf("%w: %w", errNoChildBinary, err)
		}
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(timeout)
	}

	// Killed at the timeout plus a grace, or as soon as the run is
	// canceled (worker shutdown).
	killCtx, cancelKill := context.WithDeadline(context.WithoutCancel(ctx), deadline.Add(childKillGrace))
	defer cancelKill()

	stopWatch := context.AfterFunc(ctx, func() {
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			cancelKill()
		}
	})
	defer stopWatch()

	cmd := exec.CommandContext(killCtx, exe)
	// Nothing of this process's environment (database URL, keys) reaches
	// the child.
	cmd.Env = []string{childEnvVar + "=1"}
	cmd.WaitDelay = childKillGrace

	stderr := &cappedBuffer{cap: childStderrCap}
	cmd.Stderr = stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout: %w", err)
	}

	cleanup, err := configureChildCmd(cmd, settings)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", exe, err)
	}

	outcome := &childOutcome{}
	outcome.done, outcome.protoErr = c.serve(bufio.NewReader(stdout), stdin, init)

	_ = stdin.Close()

	if outcome.done == nil {
		// Whatever the child is doing, it is over.
		cancelKill()
	}

	outcome.waitErr = cmd.Wait()
	outcome.stderr = stderr.String()
	outcome.killedAt = errors.Is(killCtx.Err(), context.DeadlineExceeded)
	outcome.canceled = errors.Is(ctx.Err(), context.Canceled)

	return outcome, nil
}

// serve sends init, then answers the child's calls until it reports its
// result or leaves.
func (c *hostChannel) serve(stdout *bufio.Reader, stdin io.Writer, init *wireInit) (*wireResult, error) {
	if err := writeFrame(stdin, &wireMsg{Init: init}); err != nil {
		return nil, err
	}

	for {
		msg, err := readChildFrame(stdout)
		if err != nil {
			return nil, err
		}

		switch {
		case msg.Done != nil:
			msg.Done.Output, _ = normalizeNumbers(msg.Done.Output).(map[string]any)
			msg.Done.Metrics, _ = normalizeNumbers(msg.Done.Metrics).(map[string]any)

			return msg.Done, nil
		case msg.Call != nil:
			if err := writeFrame(stdin, c.call(msg.Call)); err != nil {
				return nil, err
			}
		default:
			return nil, errChildProtocol
		}
	}
}

// call runs a served function for the child.
func (c *hostChannel) call(call *wireCall) *wireMsg {
	if call.ID < 1 || call.ID > len(c.handles) {
		return &wireMsg{Err: &wireErr{Name: goErrorName, Message: errUnknownHandle.Error()}}
	}

	entry := c.handles[call.ID-1]

	args, err := c.parseArgs(call)
	if err != nil {
		return &wireMsg{Err: &wireErr{Name: goErrorName, Message: err.Error()}}
	}

	val, err := entry.fn(entry.this, args...)
	if err != nil {
		return &wireMsg{Err: c.encodeError(err)}
	}

	c.nodes = 0

	node, err := c.encode(val, 0)
	if err != nil {
		return &wireMsg{Err: &wireErr{Name: goErrorName, Message: err.Error()}}
	}

	return &wireMsg{Ret: node}
}

// parseArgs rebuilds a call's arguments in the host VM with its own
// JSON.parse, so a binding sees the plain objects it would have seen.
func (c *hostChannel) parseArgs(call *wireCall) ([]goja.Value, error) {
	jsvm := c.runtime.vm

	parse, ok := goja.AssertFunction(jsvm.Get("JSON").ToObject(jsvm).Get("parse"))
	if !ok {
		return nil, errChildProtocol
	}

	parsed, err := parse(goja.Undefined(), jsvm.ToValue(call.Args))
	if err != nil {
		return nil, fmt.Errorf("arguments: %w", err)
	}

	list, ok := parsed.(*goja.Object)
	if !ok {
		return nil, errChildProtocol
	}

	length := int(list.Get("length").ToInteger())
	args := make([]goja.Value, length)

	for i := range length {
		args[i] = list.Get(strconv.Itoa(i))
	}

	for _, i := range call.Undef {
		if i >= 0 && i < length {
			args[i] = goja.Undefined()
		}
	}

	return args, nil
}

// encodeError turns what a served call threw into its wire form.
func (c *hostChannel) encodeError(err error) *wireErr {
	var exc *goja.Exception
	if !errors.As(err, &exc) || exc.Value() == nil {
		return &wireErr{Name: goErrorName, Message: err.Error()}
	}

	if obj, ok := exc.Value().(*goja.Object); ok && obj.ClassName() == "Error" {
		return &wireErr{Name: obj.Get("name").String(), Message: obj.Get("message").String()}
	}

	c.nodes = 0

	node, encErr := c.encode(exc.Value(), 0)
	if encErr != nil {
		return &wireErr{Name: goErrorName, Message: exc.Value().String()}
	}

	return &wireErr{Value: node}
}

// encode turns a host value into its wire form: data as JSON, a function as
// a handle the child calls back through.
func (c *hostChannel) encode(val goja.Value, depth int) (*wireNode, error) {
	c.nodes++
	if c.nodes > encodeMaxNodes || depth > encodeMaxDepth {
		return nil, errValueTooLarge
	}

	if val == nil || goja.IsUndefined(val) {
		return &wireNode{T: nodeUndefined}, nil
	}

	obj, isObj := val.(*goja.Object)
	if !isObj || !holdsFunction(val.Export(), 0) {
		raw, err := json.Marshal(finiteJSON(val.Export()))
		if err != nil {
			return nil, fmt.Errorf("encoding a binding result: %w", err)
		}

		return &wireNode{T: nodeValue, V: raw}, nil
	}

	return c.encodeObject(obj, depth)
}

// encodeObject encodes an object (or array) holding functions.
func (c *hostChannel) encodeObject(obj *goja.Object, depth int) (*wireNode, error) {
	if fn, ok := goja.AssertFunction(obj); ok {
		c.handles = append(c.handles, handleEntry{fn: fn, this: goja.Undefined()})

		return &wireNode{T: nodeFunction, ID: len(c.handles)}, nil
	}

	if obj.ClassName() == "Array" {
		length := int(obj.Get("length").ToInteger())
		node := &wireNode{T: nodeArray, Values: make([]*wireNode, 0, length)}

		for i := range length {
			inner, err := c.encode(obj.Get(strconv.Itoa(i)), depth+1)
			if err != nil {
				return nil, err
			}

			node.Values = append(node.Values, inner)
		}

		return node, nil
	}

	keys := obj.Keys()
	node := &wireNode{T: nodeObject, Keys: keys, Values: make([]*wireNode, 0, len(keys))}

	for _, key := range keys {
		inner := obj.Get(key)

		if fnObj, ok := inner.(*goja.Object); ok {
			if fn, isFn := goja.AssertFunction(fnObj); isFn {
				c.handles = append(c.handles, handleEntry{fn: fn, this: obj})
				node.Values = append(node.Values, &wireNode{T: nodeFunction, ID: len(c.handles)})

				continue
			}
		}

		encoded, err := c.encode(inner, depth+1)
		if err != nil {
			return nil, err
		}

		node.Values = append(node.Values, encoded)
	}

	return node, nil
}

// holdsFunction reports whether an exported value holds a function at any
// depth.
func holdsFunction(val any, depth int) bool {
	if depth > encodeMaxDepth {
		return true
	}

	switch typed := val.(type) {
	case nil, string, bool, int64, float64:
		return false
	case func(goja.FunctionCall) goja.Value:
		return true
	case map[string]any:
		for _, inner := range typed {
			if holdsFunction(inner, depth+1) {
				return true
			}
		}

		return false
	case []any:
		for _, inner := range typed {
			if holdsFunction(inner, depth+1) {
				return true
			}
		}

		return false
	default:
		// Typed Go values a binding returned as is: JSON decides. A
		// function among them is not served.
		return false
	}
}

// outcomeResult turns how the child ended into the check's result.
func (r *jsRuntime) outcomeResult(
	outcome *childOutcome, settings IsolationSettings, start time.Time, timeout time.Duration,
) *checkerdef.Result {
	if done := outcome.done; done != nil {
		status, ok := checkerdef.ParseStatus(done.Status)
		if !ok {
			status = checkerdef.StatusError
		}

		if done.Output == nil {
			done.Output = map[string]any{}
		}

		return &checkerdef.Result{
			Status:   status,
			Duration: time.Duration(done.DurationNs),
			Output:   done.Output,
			Metrics:  done.Metrics,
		}
	}

	duration := time.Since(start)

	switch {
	case outcome.canceled:
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: duration,
			Output:   r.buildOutput("script error: the run was canceled"),
		}
	case errors.Is(outcome.protoErr, errFrameTooLarge), errors.Is(outcome.protoErr, errTooManyValues):
		// Refused by this process, which then killed the child: not an
		// out-of-memory kill.
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: duration,
			Output:   r.buildOutput("script error: the script process sent " + outcome.protoErr.Error()),
		}
	case outcome.killedAt:
		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: duration,
			Output: r.buildOutput("script timed out after " + timeout.String() +
				": the script process did not return from a native call and was killed"),
		}
	case childOutOfMemory(outcome):
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: duration,
			Output: r.buildOutput(fmt.Sprintf(
				"script error: memory limit exceeded: the script process used more than its %d MiB "+
					"memory limit and was killed", settings.MemoryLimit/bytesPerMiB)),
		}
	default:
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: duration,
			Output:   r.buildOutput("script error: the script process exited unexpectedly: " + childExitReason(outcome)),
		}
	}
}

// childOutOfMemory reports whether the child died of its memory limit: its
// watchdog fired, the Go runtime could not map more memory under the
// rlimit, or the kernel (a cgroup) killed it.
func childOutOfMemory(outcome *childOutcome) bool {
	if strings.Contains(outcome.stderr, "out of memory") {
		return true
	}

	var exitErr *exec.ExitError
	if !errors.As(outcome.waitErr, &exitErr) {
		return false
	}

	if exitErr.ExitCode() == childExitMemory {
		return true
	}

	// SIGKILL the parent did not send: the OOM killer.
	status, ok := exitErr.Sys().(syscall.WaitStatus)

	return ok && status.Signaled() && status.Signal() == syscall.SIGKILL
}

// childExitReason describes an unexpected exit.
func childExitReason(outcome *childOutcome) string {
	reason := "no result"
	if outcome.waitErr != nil {
		reason = outcome.waitErr.Error()
	} else if outcome.protoErr != nil && !errors.Is(outcome.protoErr, io.EOF) {
		reason = outcome.protoErr.Error()
	}

	if line := lastLine(outcome.stderr); line != "" {
		reason += " (" + line + ")"
	}

	return reason
}

// lastLine is the line of text saying what happened: a Go panic or fatal
// error when there is one, the last line otherwise.
func lastLine(text string) string {
	for line := range strings.SplitSeq(strings.TrimSpace(text), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "panic:") || strings.HasPrefix(line, "fatal error:") {
			return line
		}
	}

	lines := strings.Split(strings.TrimSpace(text), "\n")

	return strings.TrimSpace(lines[len(lines)-1])
}
