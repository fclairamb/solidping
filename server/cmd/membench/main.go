// Command membench measures what SolidPing actually costs in memory, precisely
// enough that a claimed reduction can be believed.
//
// The problem it solves: a single /api/mgmt/memory reading a few seconds after
// boot is startup garbage plus a GC phase, and reads wildly differently ten
// minutes later. So membench boots a *fresh* server per repetition, warms it
// up, samples for a fixed window, repeats K times, and reports the median, the
// p95, the max, and — the number that matters — the **spread across
// repetitions**, which is how much the measurement moves when nothing changes.
// `--compare baseline.json` then refuses to call any delta smaller than that
// spread a result.
//
// Two modes:
//
//	--mode docker   the shipped image under `docker run --memory --cpus`:
//	                real cgroup v2 accounting, the container's Linux and libc.
//	                This is the authoritative mode.
//	--mode local    the host binary. Fast to iterate on, explicitly NOT
//	                authoritative, and every report it writes says so.
//
// Usage:
//
//	membench -mode local -scenarios idle-all-sqlite,checks-500 -label baseline
//	membench -mode docker -image ghcr.io/fclairamb/solidping:0.22.0 -label v0.22.0
//	membench -compare bench-results/memory-baseline.json -label candidate
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/fclairamb/solidping/server/internal/membench"
)

const (
	defaultWarmUp   = 60 * time.Second
	defaultInterval = 5 * time.Second
	defaultDuration = 5 * time.Minute
	defaultReps     = 3
	defaultPort     = 4009
	bootTimeout     = 3 * time.Minute
)

type options struct {
	mode      string
	image     string
	binary    string
	scenarios string
	label     string
	outDir    string
	compare   string
	memory    string
	cpus      string
	baseURL   string
	extraEnv  string
	floor     bool
	warmUp    time.Duration
	interval  time.Duration
	duration  time.Duration
	reps      int
	port      int
}

func main() {
	opts := parseFlags()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, opts); err != nil {
		fmt.Fprintf(os.Stderr, "membench: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() options {
	var opts options

	flag.StringVar(&opts.mode, "mode", "local",
		"local (host binary, NOT authoritative), docker (shipped image, authoritative), "+
			"or attach (sample a server somebody else is running, at -base-url)")
	flag.StringVar(&opts.baseURL, "base-url", "", "base URL of an already-running server, for -mode attach")
	flag.StringVar(&opts.image, "image", "ghcr.io/fclairamb/solidping:latest", "container image for -mode docker")
	flag.StringVar(&opts.binary, "binary", "./solidping", "server binary for -mode local")
	flag.StringVar(&opts.scenarios, "scenarios", "idle-all-sqlite", "comma-separated scenario names, or 'all'")
	flag.StringVar(&opts.label, "label", "baseline", "label for this run (names the output files)")
	flag.StringVar(&opts.outDir, "out-dir", "bench-results", "directory for the markdown and JSON reports")
	flag.StringVar(&opts.compare, "compare", "", "baseline JSON report to compare this run against")
	flag.StringVar(&opts.extraEnv, "env", "",
		"comma-separated KEY=VALUE overrides applied to every scenario, e.g. -env GOGC=50 "+
			"(recorded in the report, so a run tuned by env cannot be mistaken for an untuned one)")
	flag.StringVar(&opts.memory, "memory", "1g", "container memory limit for -mode docker")
	flag.StringVar(&opts.cpus, "cpus", "1", "container CPU limit for -mode docker")
	flag.DurationVar(&opts.warmUp, "warmup", defaultWarmUp, "warm-up before sampling starts")
	flag.DurationVar(&opts.interval, "interval", defaultInterval, "sampling interval")
	flag.DurationVar(&opts.duration, "duration", defaultDuration, "sampling window")
	flag.IntVar(&opts.reps, "reps", defaultReps, "repetitions per scenario (≥2 to get a spread, and without a spread nothing is significant)")
	flag.BoolVar(&opts.floor, "floor", false,
		"sample the LIVE FLOOR (?gc=1: force a GC and return free pages before each reading) instead of the "+
			"steady state. A different measurement, not a better one — never compare the two")
	flag.IntVar(&opts.port, "port", defaultPort, "port the server under measurement listens on")
	flag.Parse()

	return opts
}

func run(ctx context.Context, opts options) error {
	scenarios, err := selectScenarios(opts)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(opts.outDir, 0o750); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	host, _ := os.Hostname()

	report := &membench.Report{
		Label:     opts.label,
		Mode:      opts.mode,
		Host:      host + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")",
		GoVersion: runtime.Version(),
		StartedAt: time.Now().UTC(),
		Protocol: membench.Protocol{
			WarmUp: opts.warmUp, Interval: opts.interval, Duration: opts.duration, Reps: opts.reps,
		},
	}

	if opts.mode == "docker" {
		report.Image = opts.image
		report.Memory = opts.memory
		report.CPUs = opts.cpus
	}

	report.Env = opts.extraEnv
	if opts.floor {
		report.SampleMode = "floor"
	} else {
		report.SampleMode = "steady"
	}

	for _, sc := range scenarios {
		result, err := runScenario(ctx, opts, sc)
		if err != nil {
			return fmt.Errorf("scenario %s: %w", sc.Name, err)
		}

		report.Scenarios = append(report.Scenarios, result)
	}

	if opts.reps < 2 {
		fmt.Println("membench: WARNING — fewer than 2 repetitions, so no spread was measured " +
			"and no comparison against this report can be called significant.")
	}

	return writeOutputs(opts, report)
}

// selectScenarios resolves the -scenarios flag, refusing an unknown name rather
// than silently measuring a shorter list than the operator asked for.
func selectScenarios(opts options) ([]scenario, error) {
	available := allScenarios()

	var selected []scenario

	if opts.scenarios == "all" {
		selected = available
	} else {
		for _, name := range strings.Split(opts.scenarios, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}

			idx := slices.IndexFunc(available, func(s scenario) bool { return s.Name == name })
			if idx < 0 {
				return nil, fmt.Errorf("unknown scenario %q (available: %s)", name, scenarioNames(available))
			}

			selected = append(selected, available[idx])
		}
	}

	if len(selected) == 0 {
		return nil, errors.New("no scenarios selected")
	}

	return selected, nil
}

func scenarioNames(scenarios []scenario) string {
	names := make([]string, 0, len(scenarios))
	for _, s := range scenarios {
		names = append(names, s.Name)
	}

	return strings.Join(names, ", ")
}

// runScenario executes every repetition of one scenario.
func runScenario(ctx context.Context, opts options, sc scenario) (membench.ScenarioResult, error) {
	result := membench.ScenarioResult{Scenario: sc.Name, Mode: opts.mode}
	if sc.Caveat != "" {
		result.Notes = append(result.Notes, sc.Caveat)
	}

	if sc.LocalOnly && opts.mode != "local" {
		result.Notes = append(result.Notes, "SKIPPED in "+opts.mode+" mode: this scenario is local-only")
		result.Metrics = membench.Aggregate(nil)

		fmt.Printf("== %s: skipped (local-only)\n", sc.Name)

		return result, nil
	}

	for rep := 1; rep <= opts.reps; rep++ {
		fmt.Printf("== %s: repetition %d/%d\n", sc.Name, rep, opts.reps)

		samples, err := runRepetition(ctx, opts, sc, rep)
		if err != nil {
			return result, err
		}

		result.Runs = append(result.Runs, membench.SummariseRun(samples))
	}

	result.Metrics = membench.Aggregate(result.Runs)

	return result, nil
}

// runRepetition boots a fresh server, warms it, and samples it. Fresh every
// time: reusing a process would carry its page cache, its heap and its GC phase
// into the next repetition, which is exactly the noise this tool exists to
// measure rather than inherit.
func runRepetition(ctx context.Context, opts options, sc scenario, rep int) ([]membench.Sample, error) {
	dataDir, err := os.MkdirTemp("", "membench-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dataDir) }()

	tgt, err := newTarget(opts, sc, rep)
	if err != nil {
		return nil, err
	}
	defer tgt.Stop()

	if startErr := tgt.Start(ctx, targetEnv(opts, sc, dataDir)); startErr != nil {
		return nil, startErr
	}

	api := newClient(tgt.BaseURL())
	if healthErr := api.waitHealthy(ctx, bootTimeout); healthErr != nil {
		return nil, bootFailure(opts, tgt, healthErr)
	}

	if loginErr := api.login(ctx); loginErr != nil {
		return nil, loginErr
	}

	if sc.Prepare != nil {
		if prepErr := sc.Prepare(ctx, api); prepErr != nil {
			return nil, fmt.Errorf("prepare: %w", prepErr)
		}
	}

	loadCtx, stopLoad := context.WithCancel(ctx)
	defer stopLoad()

	loadDone := startLoad(loadCtx, sc, api)

	if warmErr := sleepCtx(ctx, opts.warmUp); warmErr != nil {
		return nil, warmErr
	}

	samples, err := sampleWindow(ctx, api, opts)

	stopLoad()
	<-loadDone

	return samples, err
}

// bootFailure enriches a failed boot with the server's own output, so the
// operator does not have to go hunting for it.
func bootFailure(opts options, tgt target, cause error) error {
	if opts.mode == "docker" {
		if dt, ok := tgt.(*dockerTarget); ok {
			return fmt.Errorf("%w\ncontainer logs:\n%s", cause, dockerLogs(dt.name))
		}
	}

	return cause
}

// startLoad runs the scenario's workload until the context is canceled,
// returning a channel closed when it has stopped.
func startLoad(ctx context.Context, sc scenario, api *client) <-chan struct{} {
	done := make(chan struct{})

	if sc.Load == nil {
		close(done)

		return done
	}

	every := sc.LoadEvery
	if every <= 0 {
		every = time.Second
	}

	go func() {
		defer close(done)

		for {
			if ctx.Err() != nil {
				return
			}

			_ = sc.Load(ctx, api)

			select {
			case <-ctx.Done():
				return
			case <-time.After(every):
			}
		}
	}()

	return done
}

// sampleWindow takes readings at the configured interval for the configured
// duration.
func sampleWindow(ctx context.Context, api *client, opts options) ([]membench.Sample, error) {
	deadline := time.Now().Add(opts.duration)

	var samples []membench.Sample

	for time.Now().Before(deadline) {
		sample, err := api.sample(ctx, opts.floor)
		if err != nil {
			return samples, err
		}

		samples = append(samples, sample)

		if err := sleepCtx(ctx, opts.interval); err != nil {
			return samples, err
		}
	}

	if len(samples) == 0 {
		return nil, errors.New("sample window produced no readings")
	}

	return samples, nil
}

// newTarget builds the local or containerized server for one repetition.
func newTarget(opts options, sc scenario, rep int) (target, error) {
	switch opts.mode {
	case "local":
		if _, err := os.Stat(opts.binary); err != nil {
			return nil, fmt.Errorf("server binary %q not found (build it with `make build-backend`): %w", opts.binary, err)
		}

		logPath := filepath.Join(opts.outDir, fmt.Sprintf("membench-%s-%s-%d.log", opts.label, sc.Name, rep))

		return &localTarget{binary: opts.binary, port: opts.port, logPath: logPath}, nil
	case "attach":
		if opts.baseURL == "" {
			return nil, errors.New("-mode attach requires -base-url")
		}

		return &attachTarget{baseURL: opts.baseURL}, nil
	case "docker":
		return &dockerTarget{
			image:  opts.image,
			name:   fmt.Sprintf("membench-%s-%d", sc.Name, opts.port),
			port:   opts.port,
			memory: opts.memory,
			cpus:   opts.cpus,
		}, nil
	default:
		return nil, fmt.Errorf("unknown mode %q (want local or docker)", opts.mode)
	}
}

// targetEnv is the base environment every scenario starts from, with the
// scenario's own overrides applied last.
func targetEnv(opts options, sc scenario, dataDir string) map[string]string {
	env := map[string]string{
		"SP_RUNMODE":       "test",
		"SP_DB_RESET":      "true",
		"SP_SERVER_LISTEN": fmt.Sprintf("0.0.0.0:%d", opts.port),
		"LOG_LEVEL":        "warn",
		// A container's hostname is a hex id, which fails the worker-slug
		// pattern and refuses the boot roughly half the time (whenever the id
		// starts with a digit). Pinning the name also keeps the worker rows
		// from one repetition out of the next one's database.
		"SP_NODE_NAME": "membench",
	}

	switch opts.mode {
	case "local":
		env["SP_DB_DIR"] = dataDir
	case "docker":
		// The image runs as nonroot with a read-only-ish /app, so the database
		// goes somewhere the container user can actually write.
		env["SP_DB_DIR"] = "/tmp/membench"
	}

	for k, v := range sc.Env {
		env[k] = v
	}

	// Operator overrides win over the scenario: that is what makes -env usable
	// for measuring a runtime knob (GOGC, GOMEMLIMIT, GOMAXPROCS) without
	// editing the scenario table.
	for _, kv := range strings.Split(opts.extraEnv, ",") {
		key, value, found := strings.Cut(strings.TrimSpace(kv), "=")
		if found {
			env[key] = value
		}
	}

	return env
}

// writeOutputs writes the markdown table and the JSON report, plus the
// comparison when a baseline was given.
func writeOutputs(opts options, report *membench.Report) error {
	base := filepath.Join(opts.outDir, "memory-"+opts.label)

	jsonPath := base + ".json"
	mdPath := base + ".md"

	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(jsonPath, raw, 0o600); err != nil {
		return err
	}

	markdown := report.Markdown()

	if opts.compare != "" {
		baseline, err := loadReport(opts.compare)
		if err != nil {
			return err
		}

		deltas := membench.Compare(baseline, report)
		markdown += "\n" + membench.CompareMarkdown(baseline, report, deltas)
	}

	if err := os.WriteFile(mdPath, []byte(markdown), 0o600); err != nil {
		return err
	}

	fmt.Printf("\n%s\n", markdown)
	fmt.Printf("membench: wrote %s and %s\n", mdPath, jsonPath)

	return nil
}

func loadReport(path string) (*membench.Report, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied path to a bench tool
	if err != nil {
		return nil, fmt.Errorf("read baseline %s: %w", path, err)
	}

	var report membench.Report
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("parse baseline %s: %w", path, err)
	}

	return &report, nil
}

// sleepCtx sleeps unless the context ends first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
