package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// procSpec describes an auxiliary child process supervised for the whole
// devloop session, parsed from a -proc flag value "name:dir:command", e.g.
// "dash0:/repo/web/dash0:bun run dev". The command is split on whitespace.
type procSpec struct {
	name string
	dir  string
	argv []string
}

var errInvalidProc = errors.New("invalid -proc value, want name:dir:command")

func parseProcSpecs(values []string) ([]procSpec, error) {
	specs := make([]procSpec, 0, len(values))
	for _, value := range values {
		spec, err := parseProcSpec(value)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func parseProcSpec(value string) (procSpec, error) {
	parts := strings.SplitN(value, ":", 3)
	if len(parts) != 3 {
		return procSpec{}, fmt.Errorf("%w: %q", errInvalidProc, value)
	}
	name, dir, command := parts[0], parts[1], parts[2]
	argv := strings.Fields(command)
	if name == "" || dir == "" || len(argv) == 0 {
		return procSpec{}, fmt.Errorf("%w: %q", errInvalidProc, value)
	}
	return procSpec{name: name, dir: dir, argv: argv}, nil
}

// stringList collects repeatable flag values.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ", ") }

func (l *stringList) Set(value string) error {
	*l = append(*l, value)
	return nil
}

// sinkLogf writes a devloop-formatted line to a specific sink, mirroring the
// standard logger's "[devloop] HH:MM:SS" prefix, so supervision events land in
// the affected process's own log file without double-printing to the terminal.
func sinkLogf(sink io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(sink, "[devloop] %s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

// supervisor owns one child process: the backend binary (restarted on every
// rebuild) or an auxiliary dev server (started once for the session). The
// child's combined stdout/stderr goes to the supervisor's sink; devloop
// outlives its children by design so shutdown output still lands in the logs.
type supervisor struct {
	name  string
	dir   string
	argv  []string
	sink  io.Writer
	grace time.Duration

	mu       sync.Mutex
	cmd      *exec.Cmd
	done     chan struct{}
	stopping bool
}

func newSupervisor(name, dir string, argv []string, sink io.Writer) *supervisor {
	return &supervisor{name: name, dir: dir, argv: argv, sink: sink, grace: graceTimeout}
}

func (s *supervisor) start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cmd := exec.CommandContext(ctx, s.argv[0], s.argv[1:]...)
	cmd.Dir = s.dir
	cmd.Stdout = s.sink
	cmd.Stderr = s.sink
	cmd.Env = os.Environ()
	// Shutdown is driven by stop() (SIGINT, grace, SIGKILL) so the child can
	// flush its graceful-shutdown output into the log; context cancellation
	// only bounds how long exit and pipe drain may take before a hard kill.
	cmd.Cancel = func() error { return os.ErrProcessDone }
	cmd.WaitDelay = s.grace
	if err := cmd.Start(); err != nil {
		return err
	}
	s.cmd = cmd
	s.stopping = false
	done := make(chan struct{})
	s.done = done
	go func() {
		err := cmd.Wait()
		s.reportExit(err)
		close(done)
	}()
	return nil
}

// reportExit loudly announces a child that died on its own; expected exits
// triggered by stop() stay quiet.
func (s *supervisor) reportExit(err error) {
	s.mu.Lock()
	stopping := s.stopping
	if !stopping {
		s.cmd = nil
	}
	s.mu.Unlock()
	if stopping {
		return
	}
	if err != nil {
		sinkLogf(s.sink, "%s exited on its own: %v (not restarted)", s.name, err)
		return
	}
	sinkLogf(s.sink, "%s exited on its own (not restarted)", s.name)
}

// markStopping silences exit reporting ahead of an orderly shutdown, so
// children already dying from a terminal-delivered Ctrl-C are not reported as
// unexpected exits.
func (s *supervisor) markStopping() {
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()
}

// stop asks the child to exit with SIGINT, waits for the grace window, then
// kills it. It returns once the child is gone and its output fully drained.
func (s *supervisor) stop() {
	s.mu.Lock()
	cmd, done := s.cmd, s.done
	s.stopping = true
	s.cmd = nil
	s.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGINT)
	select {
	case <-done:
	case <-time.After(s.grace):
		sinkLogf(s.sink, "%s did not exit within %s, killing", s.name, s.grace)
		_ = cmd.Process.Kill()
		<-done
	}
}

// stopAll flags every child as stopping first (so none of the exits get
// reported as unexpected), then stops them in parallel.
func stopAll(children []*supervisor) {
	for _, child := range children {
		child.markStopping()
	}

	var group sync.WaitGroup
	for _, child := range children {
		group.Add(1)
		go func() {
			defer group.Done()
			child.stop()
		}()
	}
	group.Wait()
}
