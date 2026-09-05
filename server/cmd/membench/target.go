package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// target is one server under measurement, started fresh for every repetition so
// no run inherits another's warm caches, page cache or GC state.
type target interface {
	// Start boots the server and returns once the process/container exists (not
	// once it is healthy — the caller polls for that).
	Start(ctx context.Context, env map[string]string) error
	// BaseURL is where the API answers.
	BaseURL() string
	// Stop tears everything down. Must be safe to call twice.
	Stop()
	// Mode is "local" or "docker" — travels into the report so no number is
	// ever quoted without knowing which one produced it.
	Mode() string
}

// localTarget runs the host binary. Fast to iterate on, and NOT authoritative:
// on macOS there is no cgroup accounting at all, GOMAXPROCS comes from the
// laptop's cores, and the SQLite driver may not be the one the image ships.
type localTarget struct {
	binary  string
	port    int
	cmd     *exec.Cmd
	logFile *os.File
	logPath string
}

// attachTarget measures a server somebody else is running: a `make dev` process,
// a port-forwarded pod, a container started by hand. It starts and stops
// nothing, so the scenario's env is ignored — the report records mode "attach"
// so nobody mistakes it for a controlled run.
type attachTarget struct {
	baseURL string
}

func (t *attachTarget) Mode() string { return "attach" }

func (t *attachTarget) BaseURL() string { return t.baseURL }

func (t *attachTarget) Start(context.Context, map[string]string) error { return nil }

func (t *attachTarget) Stop() {}

func (t *localTarget) Mode() string { return "local" }

func (t *localTarget) BaseURL() string { return fmt.Sprintf("http://127.0.0.1:%d", t.port) }

func (t *localTarget) Start(ctx context.Context, env map[string]string) error {
	logFile, err := os.Create(t.logPath)
	if err != nil {
		return fmt.Errorf("create server log: %w", err)
	}

	t.logFile = logFile

	//nolint:gosec // the binary path is an operator-supplied flag of a bench tool
	cmd := exec.CommandContext(ctx, t.binary, "serve")
	cmd.Env = append(os.Environ(), envList(env)...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", t.binary, err)
	}

	t.cmd = cmd

	return nil
}

func (t *localTarget) Stop() {
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
		_, _ = t.cmd.Process.Wait()
		t.cmd = nil
	}

	if t.logFile != nil {
		_ = t.logFile.Close()
		t.logFile = nil
	}
}

// dockerTarget runs the shipped image under a real memory and CPU limit. This
// is the authoritative mode: cgroup v2 accounting, the image's Linux, its libc,
// its SQLite driver and the GOMEMLIMIT auto-cap the container actually gets.
type dockerTarget struct {
	image  string
	name   string
	port   int
	memory string
	cpus   string
	extra  []string
}

func (t *dockerTarget) Mode() string { return "docker" }

func (t *dockerTarget) BaseURL() string { return fmt.Sprintf("http://127.0.0.1:%d", t.port) }

func (t *dockerTarget) Start(ctx context.Context, env map[string]string) error {
	// A leftover container from an interrupted run would make the port bind
	// fail with a confusing message; remove it first.
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", t.name).Run() //nolint:errcheck // best-effort cleanup

	args := make([]string, 0, 16+2*len(env)+len(t.extra))
	args = append(args,
		"run", "-d", "--name", t.name,
		"--memory", t.memory,
		// Without a matching swap limit the container can exceed --memory via
		// swap and the accounting stops meaning what it says.
		"--memory-swap", t.memory,
		"--cpus", t.cpus,
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", t.port, t.port),
	)

	for _, kv := range envList(env) {
		args = append(args, "-e", kv)
	}

	args = append(args, t.extra...)
	args = append(args, t.image)

	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker run: %w: %s", err, strings.TrimSpace(string(out)))
	}

	return nil
}

func (t *dockerTarget) Stop() {
	// Detached from ctx on purpose: teardown must still happen when the run was
	// canceled, otherwise an interrupted bench leaves a container holding the
	// port.
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_ = exec.CommandContext(stopCtx, "docker", "rm", "-f", t.name).Run() //nolint:errcheck // best-effort cleanup
}

// dockerLogs returns the container's output, for diagnosing a boot that never
// became healthy.
func dockerLogs(name string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "docker", "logs", "--tail", "40", name).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("(docker logs failed: %v)", err)
	}

	return strings.TrimSpace(string(out))
}

// envList renders an env map as KEY=VALUE, sorted so a run is reproducible and
// the command it ran can be diffed.
func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}

	sort.Strings(out)

	return out
}
