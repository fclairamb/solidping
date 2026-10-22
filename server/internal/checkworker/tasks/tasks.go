// Package tasks is the worker side of worker tasks (spec 2026-10-22-02): the
// executors a worker runs, and the loop that claims a task for each of them,
// keeps its lease alive while it runs and reports the outcome.
//
// The same loop runs on a cloud worker (in-process client) and on an agent
// (signed HTTP client); only the Client differs.
package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// ErrNotSupported is returned by a client when the server does not serve
// worker tasks (an old server answers 404). The loop reads it as "nothing to
// do" and sleeps.
var ErrNotSupported = errors.New("server does not serve worker tasks")

// Client is how a worker talks to the worker-task service.
type Client interface {
	// Claim returns one task of the given kinds, or nil when there is none.
	Claim(ctx context.Context, kinds []string) (*workertasks.Claim, error)
	Heartbeat(ctx context.Context, uid string) error
	Step(ctx context.Context, uid string, req workertasks.StepRequest) error
	Finish(ctx context.Context, uid string, body json.RawMessage) error
	Fail(ctx context.Context, uid, errorCode string) error
}

// Reporter is what an executor uses to report the slice it runs. Exactly one
// of Step, Finish and Fail ends the slice.
type Reporter interface {
	Heartbeat(ctx context.Context) error
	Step(ctx context.Context, req workertasks.StepRequest) error
	Finish(ctx context.Context, body json.RawMessage) error
	Fail(ctx context.Context, errorCode string) error
}

// Executor is the worker side of a kind.
type Executor interface {
	// Kind is the task kind it runs.
	Kind() string
	// Capability is the capability name the worker reports for it.
	Capability() string
	// Run executes one slice. Its context is canceled when the task is gone
	// (lease lost, canceled). Returning without a report fails the slice.
	Run(ctx context.Context, claim *workertasks.Claim, reporter Reporter) error
}

//nolint:gochecknoglobals // the process-wide executor registry, filled at startup
var (
	registryMu sync.RWMutex
	registry   []Executor
)

// Register adds an executor to the process registry. Called at startup.
func Register(executor Executor) {
	registryMu.Lock()
	defer registryMu.Unlock()

	for i, existing := range registry {
		if existing.Kind() == executor.Kind() {
			registry[i] = executor

			return
		}
	}

	registry = append(registry, executor)
}

// Registered returns the registered executors.
func Registered() []Executor {
	registryMu.RLock()
	defer registryMu.RUnlock()

	return append([]Executor(nil), registry...)
}

// Capabilities returns the capability names of the registered executors.
func Capabilities() []string {
	executors := Registered()
	out := make([]string, 0, len(executors))

	for _, executor := range executors {
		if name := executor.Capability(); name != "" {
			out = append(out, name)
		}
	}

	return out
}
