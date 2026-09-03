package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fclairamb/solidping/server/internal/membench"
)

// stubServer answers the three endpoints the harness needs, with a Linux-shaped
// memory payload. It lets the whole tool — boot wait, login, sampling,
// aggregation, both output files — be exercised end to end in under a second,
// without a real server binary or a container.
func stubServer(t *testing.T, anonBytes func(call int) float64) *httptest.Server {
	t.Helper()

	var calls atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("/api/mgmt/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"accessToken":"stub-token"}`))
	})
	mux.HandleFunc("/api/mgmt/memory", func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer stub-token" {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		anon := anonBytes(int(calls.Add(1)))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":{
			"runtime":{"numGoroutine":42,"classes":{"totalBytes":%[1]f,"heapLiveBytes":%[2]f}},
			"process":{"rssBytes":%[1]f,"status":{"present":true,"rssAnonBytes":%[1]f,"rssFileBytes":1000,"threads":12},
			"smaps":{"present":true,"pssBytes":%[1]f}},
			"cgroup":{"present":true,"peakBytes":%[3]f,"unreclaimableBytes":%[1]f}}}`,
			anon, anon/2, anon*1.1)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

// fastOptions is the smoke-run protocol: no warm-up, a couple of samples, two
// repetitions so a spread actually exists.
func fastOptions(t *testing.T, srv *httptest.Server, label string) options {
	t.Helper()

	return options{
		mode:      "attach",
		baseURL:   srv.URL,
		scenarios: "idle-all-sqlite",
		label:     label,
		outDir:    t.TempDir(),
		warmUp:    0,
		interval:  time.Millisecond,
		duration:  20 * time.Millisecond,
		reps:      2,
	}
}

// TestMembenchSamplesAggregatesAndWritesReports covers the sampling half of the
// spec's end-to-end clause: the tool logs in, samples, aggregates and writes
// BOTH output files with real content. It runs in `attach` mode against a stub,
// so it deliberately does NOT cover starting a server — that half is
// TestLocalTargetStartsAndStopsAProcess below, and the docker half needs a
// daemon and an image, so it is exercised by `make bench-memory`, not by `go
// test`.
func TestMembenchSamplesAggregatesAndWritesReports(t *testing.T) {
	t.Parallel()

	srv := stubServer(t, func(call int) float64 {
		// A little jitter so the medians differ between repetitions and the
		// spread is a real number rather than a degenerate zero.
		return float64(100*1024*1024 + call*1024*1024)
	})

	opts := fastOptions(t, srv, "smoke")

	if err := run(t.Context(), opts); err != nil {
		t.Fatalf("run: %v", err)
	}

	jsonPath := filepath.Join(opts.outDir, "memory-smoke.json")
	mdPath := filepath.Join(opts.outDir, "memory-smoke.md")

	raw, err := os.ReadFile(jsonPath) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("read json report: %v", err)
	}

	var report membench.Report
	if parseErr := json.Unmarshal(raw, &report); parseErr != nil {
		t.Fatalf("parse json report: %v", parseErr)
	}

	if len(report.Scenarios) != 1 {
		t.Fatalf("expected 1 scenario, got %d", len(report.Scenarios))
	}

	result := report.Scenarios[0]
	if len(result.Runs) != 2 {
		t.Fatalf("expected 2 repetitions, got %d", len(result.Runs))
	}

	primary := result.Metrics[membench.MetricCgroupUnreclaimable]
	if primary.Median <= 0 {
		t.Errorf("primary metric not populated: %+v", primary)
	}

	if !primary.SpreadKnown {
		t.Error("two repetitions must yield a known spread")
	}

	md, err := os.ReadFile(mdPath) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("read markdown report: %v", err)
	}

	if !strings.Contains(string(md), membench.MetricCgroupUnreclaimable) {
		t.Error("markdown report is missing the primary metric column")
	}
}

// TestMembenchCompareRoundTrip proves the negative control end to end through
// the CLI: a rerun of the same stub — same distribution, different individual
// readings — must be reported as not significant, and the comparison table must
// say so in words.
func TestMembenchCompareRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	srv := stubServer(t, func(call int) float64 {
		return float64(100*1024*1024 + (call%3)*1024*1024)
	})

	baseOpts := fastOptions(t, srv, "base")
	baseOpts.outDir = dir

	if err := run(t.Context(), baseOpts); err != nil {
		t.Fatalf("baseline run: %v", err)
	}

	rerunOpts := fastOptions(t, srv, "rerun")
	rerunOpts.outDir = dir
	rerunOpts.compare = filepath.Join(dir, "memory-base.json")

	if err := run(t.Context(), rerunOpts); err != nil {
		t.Fatalf("rerun: %v", err)
	}

	md, err := os.ReadFile(filepath.Join(dir, "memory-rerun.md")) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("read comparison: %v", err)
	}

	if !strings.Contains(string(md), "membench comparison") {
		t.Fatal("comparison section missing from the report")
	}

	if !strings.Contains(string(md), "not significant") {
		t.Error("a no-op rerun must be reported as not significant somewhere in the table")
	}
}

func TestSelectScenariosRejectsUnknownName(t *testing.T) {
	t.Parallel()

	_, err := selectScenarios(options{scenarios: "does-not-exist"})
	if err == nil {
		t.Fatal("an unknown scenario name must be an error, not a silently shorter run")
	}

	if !strings.Contains(err.Error(), "available:") {
		t.Errorf("error should list the available scenarios, got %q", err)
	}

	all, err := selectScenarios(options{scenarios: "all"})
	if err != nil {
		t.Fatalf("selecting all: %v", err)
	}

	if len(all) != len(allScenarios()) {
		t.Errorf("'all' selected %d of %d scenarios", len(all), len(allScenarios()))
	}
}

// TestLocalOnlyScenarioSkippedInDocker pins that an unmeasurable scenario is
// recorded as skipped with a reason, never dropped from the report.
func TestLocalOnlyScenarioSkippedInDocker(t *testing.T) {
	t.Parallel()

	var target scenario

	for _, sc := range allScenarios() {
		if sc.LocalOnly {
			target = sc

			break
		}
	}

	if target.Name == "" {
		t.Skip("no local-only scenario declared")
	}

	result, err := runScenario(context.Background(), options{mode: "docker", reps: 3}, target)
	if err != nil {
		t.Fatalf("runScenario: %v", err)
	}

	if len(result.Runs) != 0 {
		t.Errorf("expected no repetitions for a skipped scenario, got %d", len(result.Runs))
	}

	var sawSkip bool

	for _, note := range result.Notes {
		if strings.Contains(note, "SKIPPED") {
			sawSkip = true
		}
	}

	if !sawSkip {
		t.Error("a skipped scenario must say so in its notes")
	}
}

// TestEnvListIsSorted keeps a run's command reproducible and diffable.
func TestEnvListIsSorted(t *testing.T) {
	t.Parallel()

	got := envList(map[string]string{"B": "2", "A": "1", "C": "3"})

	want := []string{"A=1", "B=2", "C=3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("envList = %v, want %v", got, want)
		}
	}
}

// TestLocalTargetStartsAndStopsAProcess exercises localTarget.Start/Stop for
// real, which the attach-mode end-to-end test cannot: it spawns an actual
// process, passes the scenario env to it, and redirects its output to the run
// log an operator would read after a failed boot.
//
// /bin/sh stands in for the server binary — the target's contract is "run
// <binary> serve with this env and capture its output", and that is exactly
// what is asserted. Booting the real server here would make a unit test depend
// on a built binary and a free port.
func TestLocalTargetStartsAndStopsAProcess(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-server.sh")

	// Echoes its argv and one env var, so both halves of the contract are
	// visible in the captured log.
	const body = "#!/bin/sh\necho \"argv=$*\"\necho \"role=$SP_NODE_ROLE\"\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatalf("write fake server: %v", err)
	}

	logPath := filepath.Join(dir, "run.log")
	target := &localTarget{binary: script, port: 4099, logPath: logPath}

	if got := target.Mode(); got != "local" {
		t.Errorf("Mode() = %q, want local", got)
	}

	if got := target.BaseURL(); got != "http://127.0.0.1:4099" {
		t.Errorf("BaseURL() = %q", got)
	}

	if err := target.Start(t.Context(), map[string]string{"SP_NODE_ROLE": "api,checks"}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// The process is short-lived; wait for it to write its output.
	var captured string

	for range 50 {
		raw, err := os.ReadFile(logPath) //nolint:gosec // test-controlled path
		if err == nil && strings.Contains(string(raw), "role=") {
			captured = string(raw)

			break
		}

		time.Sleep(20 * time.Millisecond)
	}

	if !strings.Contains(captured, "argv=serve") {
		t.Errorf("the binary must be invoked as `<binary> serve`; log was %q", captured)
	}

	if !strings.Contains(captured, "role=api,checks") {
		t.Errorf("the scenario env must reach the process; log was %q", captured)
	}

	// Stop must be safe twice: it runs from a defer and again on a later error
	// path.
	target.Stop()
	target.Stop()
}

// TestLocalTargetRejectsMissingBinary pins the error an operator actually hits
// — running `make bench-memory` before `make build-backend`.
func TestLocalTargetRejectsMissingBinary(t *testing.T) {
	t.Parallel()

	_, err := newTarget(options{mode: "local", binary: filepath.Join(t.TempDir(), "nope"), outDir: t.TempDir()},
		allScenarios()[0], 1)
	if err == nil {
		t.Fatal("a missing server binary must be an error")
	}

	if !strings.Contains(err.Error(), "make build-backend") {
		t.Errorf("the error should name the fix, got %q", err)
	}
}

// TestAttachTargetRequiresBaseURL keeps the third mode from silently pointing at
// nothing.
func TestAttachTargetRequiresBaseURL(t *testing.T) {
	t.Parallel()

	if _, err := newTarget(options{mode: "attach"}, allScenarios()[0], 1); err == nil {
		t.Error("-mode attach without -base-url must be an error")
	}

	target, err := newTarget(options{mode: "attach", baseURL: "http://example.invalid"}, allScenarios()[0], 1)
	if err != nil {
		t.Fatalf("newTarget: %v", err)
	}

	if got := target.Mode(); got != "attach" {
		t.Errorf("Mode() = %q, want attach", got)
	}
}
