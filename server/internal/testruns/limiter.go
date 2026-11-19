// Package testruns is the admission control of every on-demand script run
// the API node executes for a tenant: the REST test run (POST
// /orgs/:org/checks/test-run), the MCP probes (run_js_script, fetch_page,
// browser_snapshot) and the runs of the AI authoring loop (spec
// 2026-11-19-03). One Limiter is built per process and shared by all of
// them, so a tenant cannot add its REST, MCP and AI runs together to get
// past the caps.
package testruns

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	// MaxConcurrent bounds the runs a process executes at once: each may hold
	// a browser slot and a 256 MB child process.
	MaxConcurrent = 4
	// MaxPerOrg bounds the runs one org has in flight at once (spec
	// 2026-11-18-01), so a single tenant can never hold every process slot.
	MaxPerOrg = 1
	// RunsPerMinute is the per-org allowance of Allow. Fixed, not plan-based.
	RunsPerMinute = 20
	// RetryAfter is the wait a refused caller is told to observe.
	RetryAfter = 5 * time.Second

	// waitPoll is how often AcquireWait retries a refused acquisition.
	waitPoll = 250 * time.Millisecond
	// rateWindow is the window RunsPerMinute counts in.
	rateWindow = time.Minute
	// pruneAbove is the map size above which Allow drops expired windows.
	pruneAbove = 1024
)

// The reasons Acquire and Allow refuse a run.
var (
	ErrOrgBusy     = errors.New("org at its run limit")
	ErrProcessBusy = errors.New("every run slot is busy")
	ErrRateLimited = errors.New("org at its runs-per-minute limit")
)

// rateWindowState is one org's fixed-window counter.
type rateWindowState struct {
	start time.Time
	count int
}

// Limiter admits runs under a process cap, a per-org concurrency cap and a
// per-org rate. The zero value is not usable: build it with New or NewLimiter.
type Limiter struct {
	slots     chan struct{}
	maxPerOrg int
	perMinute int
	now       func() time.Time

	mu sync.Mutex
	// perOrg counts the runs in flight by org key. An entry is deleted when
	// its count drops back to zero, so the map only holds busy orgs.
	perOrg  map[string]int
	windows map[string]*rateWindowState
}

// New builds the limiter with the default caps.
func New() *Limiter {
	return NewLimiter(MaxConcurrent, MaxPerOrg, RunsPerMinute)
}

// NewLimiter builds a limiter with explicit caps.
func NewLimiter(maxConcurrent, maxPerOrg, perMinute int) *Limiter {
	return &Limiter{
		slots:     make(chan struct{}, maxConcurrent),
		maxPerOrg: maxPerOrg,
		perMinute: perMinute,
		now:       time.Now,
		perOrg:    map[string]int{},
		windows:   map[string]*rateWindowState{},
	}
}

// Acquire reserves one of the org's slots and one of the process's slots,
// without waiting. The returned release frees both and may be called more
// than once.
func (l *Limiter) Acquire(org string) (func(), error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.perOrg[org] >= l.maxPerOrg {
		return nil, ErrOrgBusy
	}

	select {
	case l.slots <- struct{}{}:
	default:
		return nil, ErrProcessBusy
	}

	l.perOrg[org]++

	var once sync.Once

	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()

			l.perOrg[org]--
			if l.perOrg[org] <= 0 {
				delete(l.perOrg, org)
			}

			<-l.slots
		})
	}, nil
}

// AcquireWait is Acquire retried until it succeeds or ctx is done, for a
// caller that runs in the background of a longer request (the AI loop).
func (l *Limiter) AcquireWait(ctx context.Context, org string) (func(), error) {
	ticker := time.NewTicker(waitPoll)
	defer ticker.Stop()

	for {
		release, err := l.Acquire(org)
		if err == nil {
			return release, nil
		}

		select {
		case <-ctx.Done():
			return nil, errors.Join(err, ctx.Err())
		case <-ticker.C:
		}
	}
}

// Allow counts one run against the org's RunsPerMinute allowance, and
// reports false when the allowance of the current minute is spent.
func (l *Limiter) Allow(org string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()

	if len(l.windows) > pruneAbove {
		for key, state := range l.windows {
			if now.Sub(state.start) >= rateWindow {
				delete(l.windows, key)
			}
		}
	}

	state, ok := l.windows[org]
	if !ok || now.Sub(state.start) >= rateWindow {
		state = &rateWindowState{start: now}
		l.windows[org] = state
	}

	if state.count >= l.perMinute {
		return false
	}

	state.count++

	return true
}

// BusyOrgs reports how many orgs have a run in flight.
func (l *Limiter) BusyOrgs() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.perOrg)
}

// Slots is the process semaphore: one entry per run in flight. Tests fill
// it to simulate a busy process.
func (l *Limiter) Slots() chan struct{} {
	return l.slots
}
