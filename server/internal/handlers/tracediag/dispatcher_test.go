package tracediag

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/nettrace"
)

type recordingStore struct {
	mu      sync.Mutex
	puts    []storedTrace
	failPut error
}

type storedTrace struct {
	incidentUID string
	body        []byte
	details     models.JSONMap
}

func (r *recordingStore) PutIncidentTraceroute(
	_ context.Context, _, incidentUID string, capture []byte, details models.JSONMap,
) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.failPut != nil {
		return "", r.failPut
	}

	r.puts = append(r.puts, storedTrace{incidentUID: incidentUID, body: capture, details: details})

	return "file-" + incidentUID, nil
}

func (r *recordingStore) snapshot() []storedTrace {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]storedTrace(nil), r.puts...)
}

type recordingAgent struct {
	mu       sync.Mutex
	accept   bool
	requests []*incidents.TraceRequest
}

func (a *recordingAgent) SendTraceRequest(_ context.Context, _ string, req *incidents.TraceRequest) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.requests = append(a.requests, req)

	return a.accept
}

func (a *recordingAgent) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return len(a.requests)
}

func testConfig() config.TracerouteConfig {
	return config.TracerouteConfig{
		Enabled: true,
		Rounds:  2,
		Hops:    5,
		Budget:  2 * time.Second,
		Limit:   3,
	}
}

func testRequest() *incidents.TraceRequest {
	return &incidents.TraceRequest{
		OrgUID:      "org-1",
		IncidentUID: "incident-1",
		CheckUID:    "check-1",
		WorkerUID:   "worker-1",
		Region:      "eu2",
		Trigger:     "incident-open",
		Topic:       "incidents/incident-1/traceroute",
		Failure: checkerdef.NetworkFailure{
			Class:   checkerdef.NetFailureConnectTimeout,
			Host:    "acme.com",
			Address: "192.0.2.10",
			Port:    443,
		},
	}
}

// newLocalDispatcher builds a dispatcher that traces in-process with a stubbed
// prober, and returns a channel that fires once each dispatched trace finishes.
func newLocalDispatcher(
	t *testing.T, store AttachmentWriter,
	run func(ctx context.Context, opts *nettrace.Options) (*nettrace.Capture, error),
) (*Dispatcher, chan struct{}) {
	t.Helper()

	dispatcher := New(testConfig(), store, nil)
	dispatcher.SetLocalWorkerResolver(LocalWorkerFunc(func(string) bool { return true }))
	dispatcher.run = run

	done := make(chan struct{}, 16)
	dispatcher.done = done

	return dispatcher, done
}

func waitFor(t *testing.T, done chan struct{}) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatched trace never finished")
	}
}

func stubCapture(_ context.Context, opts *nettrace.Options) (*nettrace.Capture, error) {
	return &nettrace.Capture{
		Mode:                nettrace.ModeICMPRaw,
		HopAddressesVisible: true,
		Host:                opts.Host,
		Address:             opts.Address.String(),
		Family:              "ipv4",
		Port:                opts.Port,
		Rounds:              opts.Rounds,
		MaxHops:             opts.MaxHops,
		StartedAt:           time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC),
		Complete:            true,
		Hops: []nettrace.Hop{
			{TTL: 1, Address: "10.0.0.1", Sent: 2, Received: 2, RTTAvgMs: 1.5},
			{TTL: 2, Address: opts.Address.String(), Sent: 2, Received: 2, RTTAvgMs: 12, Final: true},
		},
	}, nil
}

func TestDispatcherStoresALocalCapture(t *testing.T) {
	t.Parallel()

	store := &recordingStore{}
	dispatcher, done := newLocalDispatcher(t, store, stubCapture)

	dispatcher.RequestTrace(t.Context(), testRequest())
	waitFor(t, done)

	puts := store.snapshot()
	if len(puts) != 1 {
		t.Fatalf("stored %d captures, want 1", len(puts))
	}

	capture, err := nettrace.ParseCapture(puts[0].body)
	if err != nil {
		t.Fatalf("the stored bytes are not a parseable capture: %v", err)
	}

	// Region and trigger are stamped by the DISPATCHER from the incident
	// pipeline's view, never by the tracer: a deported agent must not be the
	// authority on where it ran.
	if capture.Region != "eu2" || capture.Trigger != "incident-open" {
		t.Fatalf("region/trigger not stamped: %+v", capture)
	}

	if capture.Address != "192.0.2.10" || capture.Port != 443 {
		t.Fatalf("the trace did not follow the failing probe's endpoint: %+v", capture)
	}

	details := puts[0].details
	if details["checkUid"] != "check-1" || details["trigger"] != "incident-open" || details["region"] != "eu2" {
		t.Fatalf("details bag is wrong: %+v", details)
	}
}

// TestDispatcherReturnsBeforeTheTraceFinishes is the spec's "best-effort and
// asynchronous" requirement as an assertion: the incident pipeline calls this
// synchronously, so a trace that blocked would add its whole budget to every
// incident open.
func TestDispatcherReturnsBeforeTheTraceFinishes(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	store := &recordingStore{}

	dispatcher, done := newLocalDispatcher(t, store,
		func(ctx context.Context, opts *nettrace.Options) (*nettrace.Capture, error) {
			<-release

			return stubCapture(ctx, opts)
		})

	start := time.Now()
	dispatcher.RequestTrace(t.Context(), testRequest())
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Fatalf("RequestTrace blocked for %s — it must return before the trace runs", elapsed)
	}

	if got := store.snapshot(); len(got) != 0 {
		t.Fatalf("the capture was stored synchronously: %+v", got)
	}

	close(release)
	waitFor(t, done)

	if got := store.snapshot(); len(got) != 1 {
		t.Fatalf("the trace never completed: %d stored", len(got))
	}
}

// TestDispatcherSurvivesAFailingTrace / ...AFailingStore — the two ways the
// capture can be lost. Neither may panic, and neither has anything to report:
// the incident is already open and correct.
func TestDispatcherSurvivesAFailingTrace(t *testing.T) {
	t.Parallel()

	store := &recordingStore{}
	dispatcher, done := newLocalDispatcher(t, store,
		func(context.Context, *nettrace.Options) (*nettrace.Capture, error) {
			return nil, nettrace.ErrNoModeAvailable
		})

	dispatcher.RequestTrace(t.Context(), testRequest())
	waitFor(t, done)

	if got := store.snapshot(); len(got) != 0 {
		t.Fatalf("a failed trace stored something: %+v", got)
	}
}

func TestDispatcherSurvivesAFailingStore(t *testing.T) {
	t.Parallel()

	store := &recordingStore{failPut: errStoreDown}
	dispatcher, done := newLocalDispatcher(t, store, stubCapture)

	dispatcher.RequestTrace(t.Context(), testRequest())
	waitFor(t, done)
}

var errStoreDown = errors.New("storage down")

// TestDispatcherIsInertWhenDisabled — the deployment kill switch.
func TestDispatcherIsInertWhenDisabled(t *testing.T) {
	t.Parallel()

	store := &recordingStore{}
	cfg := testConfig()
	cfg.Enabled = false

	dispatcher := New(cfg, store, nil)
	dispatcher.SetLocalWorkerResolver(LocalWorkerFunc(func(string) bool { return true }))
	dispatcher.run = stubCapture

	done := make(chan struct{}, 4)
	dispatcher.done = done

	dispatcher.RequestTrace(t.Context(), testRequest())

	select {
	case <-done:
		t.Fatal("a disabled dispatcher ran a trace")
	case <-time.After(200 * time.Millisecond):
	}

	if got := store.snapshot(); len(got) != 0 {
		t.Fatalf("a disabled dispatcher stored something: %+v", got)
	}
}

// TestDispatcherPrefersTheAgent pins the routing rule: the agent that produced
// the failing result is the only host whose path to the target is the one that
// broke, so when it is reachable nothing runs here.
func TestDispatcherPrefersTheAgent(t *testing.T) {
	t.Parallel()

	store := &recordingStore{}
	agent := &recordingAgent{accept: true}

	dispatcher, done := newLocalDispatcher(t, store, stubCapture)
	dispatcher.SetAgentSender(agent)

	dispatcher.RequestTrace(t.Context(), testRequest())

	select {
	case <-done:
		t.Fatal("a local trace ran even though the agent accepted the request")
	case <-time.After(200 * time.Millisecond):
	}

	if agent.count() != 1 {
		t.Fatalf("the agent was asked %d times, want 1", agent.count())
	}

	if got := store.snapshot(); len(got) != 0 {
		t.Fatalf("the server stored a capture it should never have run: %+v", got)
	}
}

// TestDispatcherDropsAnUnreachableAgentRatherThanTracingLocally is the negative
// that keeps the feature HONEST. An agent whose connection is gone leaves the
// server with no vantage point; tracing from here would produce a route the
// probe never took and attach it to the incident as if it had.
func TestDispatcherDropsAnUnreachableAgentRatherThanTracingLocally(t *testing.T) {
	t.Parallel()

	store := &recordingStore{}
	agent := &recordingAgent{accept: false}

	dispatcher := New(testConfig(), store, nil)
	dispatcher.SetAgentSender(agent)
	dispatcher.run = stubCapture

	done := make(chan struct{}, 4)
	dispatcher.done = done

	// NO local resolver: this process runs no checks of its own.
	dispatcher.RequestTrace(t.Context(), testRequest())

	select {
	case <-done:
		t.Fatal("traced from the wrong host after the agent was unreachable")
	case <-time.After(200 * time.Millisecond):
	}

	if got := store.snapshot(); len(got) != 0 {
		t.Fatalf("stored a capture from the wrong vantage point: %+v", got)
	}

	// Positive control: with a local resolver that claims the worker, the very
	// same request DOES trace — so the drop above is the vantage-point rule and
	// not the dispatcher being broken.
	dispatcher.SetLocalWorkerResolver(LocalWorkerFunc(func(string) bool { return true }))
	dispatcher.RequestTrace(t.Context(), testRequest())
	waitFor(t, done)

	if got := store.snapshot(); len(got) != 1 {
		t.Fatalf("the positive control did not trace: %d stored", len(got))
	}
}

// TestDispatcherRefusesAMarkerWithNoAddress: a marker whose address could not
// be observed is dropped rather than re-resolved from the hostname, which could
// point the trace at a machine that never failed.
func TestDispatcherRefusesAMarkerWithNoAddress(t *testing.T) {
	t.Parallel()

	store := &recordingStore{}
	dispatcher, done := newLocalDispatcher(t, store, stubCapture)

	req := testRequest()
	req.Failure.Address = ""

	dispatcher.RequestTrace(t.Context(), req)

	select {
	case <-done:
		t.Fatal("traced a target with no address")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestDispatcherRateLimitsPerOrganization is the mass-outage guard: when one
// upstream drops, every check behind it opens an incident inside the same
// minute, and without this each would fork its own 15-second sweep.
func TestDispatcherRateLimitsPerOrganization(t *testing.T) {
	t.Parallel()

	store := &recordingStore{}
	dispatcher, done := newLocalDispatcher(t, store, stubCapture)

	// Limit is 3 per minute per org.
	for i := range 5 {
		req := testRequest()
		req.IncidentUID = "incident-" + string(rune('a'+i))
		dispatcher.RequestTrace(t.Context(), req)
	}

	for range 3 {
		waitFor(t, done)
	}

	select {
	case <-done:
		t.Fatal("a fourth trace ran inside the same minute")
	case <-time.After(200 * time.Millisecond):
	}

	if got := store.snapshot(); len(got) != 3 {
		t.Fatalf("stored %d captures, want exactly the limit (3)", len(got))
	}

	// Positive control: a DIFFERENT org has its own budget, so the ceiling is
	// per-organization and not a global one.
	other := testRequest()
	other.OrgUID = "org-2"
	other.IncidentUID = "incident-other"
	dispatcher.RequestTrace(t.Context(), other)
	waitFor(t, done)

	if got := store.snapshot(); len(got) != 4 {
		t.Fatalf("a second org was refused by the first org's budget: %d stored", len(got))
	}
}

func TestOrgLimiterWindows(t *testing.T) {
	t.Parallel()

	limiter := newOrgLimiter(2)
	minute := time.Date(2026, 8, 21, 12, 0, 30, 0, time.UTC)

	if !limiter.allow("org", minute) {
		t.Fatal("the first trace in a minute must be admitted")
	}

	if !limiter.allow("org", minute) {
		t.Fatal("the second trace in a minute must be admitted")
	}

	if limiter.allow("org", minute.Add(20*time.Second)) {
		t.Fatal("the third trace inside the SAME minute must be refused")
	}

	// The next minute is a fresh budget — otherwise a single burst would
	// disable diagnostics for the rest of the process's life.
	if !limiter.allow("org", minute.Add(time.Minute)) {
		t.Fatal("the next minute must start a fresh budget")
	}
}

func TestOrgLimiterZeroMeansUnlimited(t *testing.T) {
	t.Parallel()

	limiter := newOrgLimiter(0)
	now := time.Now()

	for range 100 {
		if !limiter.allow("org", now) {
			t.Fatal("limit 0 must disable limiting entirely")
		}
	}
}

// TestCaptureOptionsCarryTheConfiguredBudget makes sure the operator's knobs
// actually reach the prober rather than silently defaulting.
func TestCaptureOptionsCarryTheConfiguredBudget(t *testing.T) {
	t.Parallel()

	var seen *nettrace.Options

	store := &recordingStore{}
	dispatcher, done := newLocalDispatcher(t, store,
		func(ctx context.Context, opts *nettrace.Options) (*nettrace.Capture, error) {
			seen = opts

			return stubCapture(ctx, opts)
		})

	dispatcher.RequestTrace(t.Context(), testRequest())
	waitFor(t, done)

	if seen == nil {
		t.Fatal("the prober was never called")
	}

	if seen.Rounds != 2 || seen.MaxHops != 5 || seen.Budget != 2*time.Second {
		t.Fatalf("config did not reach the prober: %+v", seen)
	}

	if !seen.Address.Equal(net.ParseIP("192.0.2.10")) {
		t.Fatalf("address = %v, want the failing probe's", seen.Address)
	}
}

// TestConfigDefaultsMatchTheProber pins the one duplication in this feature.
//
// config is a leaf package that nothing else may depend on, so
// CheckersConfig.TraceroutePolicy() carries its OWN copies of the prober's
// default rounds / hops / budget rather than importing them. That is a real
// risk of drift — an operator who never sets the env vars would silently get
// different settings from a self-hoster who set them to "the default" — and
// this is the test that makes the drift impossible to land unnoticed.
func TestConfigDefaultsMatchTheProber(t *testing.T) {
	t.Parallel()

	var checkers config.CheckersConfig

	policy := checkers.TraceroutePolicy()

	if policy.Rounds != nettrace.DefaultRounds {
		t.Fatalf("config default rounds = %d, prober default = %d", policy.Rounds, nettrace.DefaultRounds)
	}

	if policy.Hops != nettrace.DefaultMaxHops {
		t.Fatalf("config default hops = %d, prober default = %d", policy.Hops, nettrace.DefaultMaxHops)
	}

	if policy.Budget != nettrace.DefaultBudget {
		t.Fatalf("config default budget = %s, prober default = %s", policy.Budget, nettrace.DefaultBudget)
	}
}

// TestLoadedConfigDefaultsAreTheSame closes the other half: the values baked
// into config.Load()'s defaults block, which an operator gets with no env vars
// set at all.
//
//nolint:paralleltest // config.Load reads process environment; keep it serial
func TestLoadedConfigDefaultsAreTheSame(t *testing.T) {
	// Deliberately NOT parallel: config.Load reads process environment.
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	policy := loaded.Checkers.TraceroutePolicy()

	if !policy.Enabled {
		t.Fatal("traceroute must be ON by deployment default (the spec's org-level default is on)")
	}

	if policy.Rounds != nettrace.DefaultRounds ||
		policy.Hops != nettrace.DefaultMaxHops ||
		policy.Budget != nettrace.DefaultBudget {
		t.Fatalf("loaded defaults drifted from the prober: %+v", policy)
	}

	if policy.Limit <= 0 {
		t.Fatal("the per-organization rate limit must have a real default: 0 disables the mass-outage guard")
	}
}

// TestDispatcherSurvivesAPanickingProber is the hardest form of "a trace
// failure never affects the incident".
//
// Errors on this path are already swallowed everywhere — RequestTrace returns
// nothing, every store and prober error is logged and dropped — but a panic on
// a detached goroutine is not an error: unrecovered, it takes the whole process
// down, and with it every check the worker was running. A best-effort
// diagnostic must not be able to do that, and the prober parses ICMP bytes
// chosen by whatever sits on the path.
func TestDispatcherSurvivesAPanickingProber(t *testing.T) {
	t.Parallel()

	store := &recordingStore{}
	dispatcher, done := newLocalDispatcher(t, store,
		func(context.Context, *nettrace.Options) (*nettrace.Capture, error) {
			panic("a malformed ICMP reply blew up the parser")
		})

	dispatcher.RequestTrace(t.Context(), testRequest())
	waitFor(t, done)

	if got := store.snapshot(); len(got) != 0 {
		t.Fatalf("a panicking trace stored something: %+v", got)
	}

	// The dispatcher is still usable afterwards: a panic must not poison it.
	dispatcher.run = stubCapture
	dispatcher.RequestTrace(t.Context(), testRequest())
	waitFor(t, done)

	if got := store.snapshot(); len(got) != 1 {
		t.Fatalf("the dispatcher stopped working after a panic: %d stored", len(got))
	}
}
