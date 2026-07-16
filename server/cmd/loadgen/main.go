// Command loadgen drives a running SolidPing server with N synthetic
// HTTP checks, lets them run for some duration, and writes a markdown
// report of throughput and latency drawn from /metrics. It does not
// start the server itself — that's the Makefile's job. The companion
// `make bench-checks` target launches a test-mode server with the
// requested backend, runs this binary, then shuts the server down.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// nameValidationScheme is what we pass to expfmt.NewTextParser. The
// default-zero ValidationScheme on a TextParser{} panics with "unset"
// at scrape time on prometheus/common v0.66+, so we must pick one
// explicitly. UTF8Validation is the upstream default and matches what
// server-side client_golang emits.
var nameValidationScheme = model.UTF8Validation

const (
	defaultAPIURL    = "http://localhost:4000"
	defaultChecks    = 100
	defaultDuration  = 2 * time.Minute
	defaultPeriod    = "10s"
	defaultLatency   = 10 * time.Millisecond
	defaultOutputDir = "bench-results"
	benchOrg         = "test"
	benchEmail       = "test@test.com"
	benchPassword    = "test"
	healthTimeout    = 30 * time.Second
)

func main() {
	apiURL := flag.String("api-url", defaultAPIURL, "Base URL of the running solidping server (test mode)")
	backend := flag.String("backend", "unknown", "Backend label used in the report filename (sqlite, postgres, ...)")
	nChecks := flag.Int("checks", defaultChecks, "Number of HTTP checks to create")
	dur := flag.Duration("duration", defaultDuration, "How long to run the load")
	period := flag.String("period", defaultPeriod, "Check period (Go duration string)")
	targetLatency := flag.Duration(
		"target-latency", defaultLatency,
		"Latency the in-process target HTTP server adds before responding",
	)
	outputDir := flag.String("output-dir", defaultOutputDir, "Directory to write the markdown report to")
	keep := flag.Bool("keep-checks", false, "Skip deleting checks at the end (for debugging)")
	flag.Parse()

	if err := run(runOpts{
		apiURL:        *apiURL,
		backend:       *backend,
		nChecks:       *nChecks,
		duration:      *dur,
		period:        *period,
		targetLatency: *targetLatency,
		outputDir:     *outputDir,
		keep:          *keep,
	}); err != nil {
		log.Fatalf("loadgen failed: %v", err)
	}
}

type runOpts struct {
	apiURL        string
	backend       string
	nChecks       int
	duration      time.Duration
	period        string
	targetLatency time.Duration
	outputDir     string
	keep          bool
}

func run(opts runOpts) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	log.Printf(
		"loadgen: api=%s backend=%s checks=%d duration=%s period=%s target-latency=%s",
		opts.apiURL, opts.backend, opts.nChecks, opts.duration, opts.period, opts.targetLatency,
	)

	if err := waitForHealth(ctx, opts.apiURL); err != nil {
		return fmt.Errorf("server health check failed: %w", err)
	}

	target := startTargetServer(opts.targetLatency)
	defer target.Close()
	log.Printf("loadgen: target server at %s", target.URL)

	token, err := login(ctx, opts.apiURL)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}

	if err := deleteAllChecks(ctx, opts.apiURL, token); err != nil {
		log.Printf("loadgen: pre-clean failed (often harmless on first run): %v", err)
	}
	if !opts.keep {
		defer func() {
			cleanCtx, c := context.WithTimeout(context.Background(), 30*time.Second)
			defer c()
			if err := deleteAllChecks(cleanCtx, opts.apiURL, token); err != nil {
				log.Printf("loadgen: post-clean failed: %v", err)
			}
		}()
	}

	if err := bulkCreateChecks(ctx, opts.apiURL, token, opts.nChecks, opts.period, target.URL); err != nil {
		return fmt.Errorf("bulk create: %w", err)
	}
	log.Printf("loadgen: created %d checks", opts.nChecks)

	baseline, err := scrapeMetrics(ctx, opts.apiURL)
	if err != nil {
		return fmt.Errorf("baseline scrape: %w", err)
	}

	startTime := time.Now()
	log.Printf("loadgen: running load for %s ...", opts.duration)
	select {
	case <-ctx.Done():
		log.Printf("loadgen: interrupted; finishing early")
	case <-time.After(opts.duration):
	}
	elapsed := time.Since(startTime)

	final, err := scrapeMetrics(context.Background(), opts.apiURL)
	if err != nil {
		return fmt.Errorf("final scrape: %w", err)
	}

	report := buildReport(opts, elapsed, baseline, final)

	if err := os.MkdirAll(opts.outputDir, 0o755); err != nil {
		return err
	}
	stamp := time.Now().Format("20060102-150405")
	outputPath := filepath.Join(opts.outputDir, fmt.Sprintf("%s-%s.md", stamp, opts.backend))
	if err := os.WriteFile(outputPath, []byte(report), 0o600); err != nil {
		return err
	}
	log.Printf("loadgen: wrote %s", outputPath)
	fmt.Println(report)
	return nil
}

func startTargetServer(latency time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if latency > 0 {
			time.Sleep(latency)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
}

func waitForHealth(ctx context.Context, apiURL string) error {
	deadline := time.Now().Add(healthTimeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL+"/api/mgmt/health", nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("server at %s did not become healthy within %s", apiURL, healthTimeout)
}

func login(ctx context.Context, apiURL string) (string, error) {
	body := map[string]string{"email": benchEmail, "password": benchPassword, "org": benchOrg}
	buf, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+"/api/v1/auth/login", bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("login returned %d: %s", resp.StatusCode, raw)
	}
	var parsed struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	if parsed.AccessToken == "" {
		return "", errors.New("login response had empty accessToken")
	}
	return parsed.AccessToken, nil
}

func deleteAllChecks(ctx context.Context, apiURL, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, apiURL+"/api/v1/test/checks/all", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("delete-all returned %d", resp.StatusCode)
	}
	return nil
}

func bulkCreateChecks(ctx context.Context, apiURL, token string, count int, period, targetURL string) error {
	q := fmt.Sprintf(
		"type=http&slug=loadgen-{nb}&count=%d&period=%s&url=%s&org=%s",
		count, period, targetURL, benchOrg,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+"/api/v1/test/checks/bulk?"+q, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bulk-create returned %d: %s", resp.StatusCode, raw)
	}
	return nil
}

// metricsSnapshot is the parsed view of /metrics at a point in time.
// Counters are kept as their cumulative value; we subtract baseline from
// final in buildReport.
type metricsSnapshot struct {
	families map[string]*dto.MetricFamily
}

func scrapeMetrics(ctx context.Context, apiURL string) (*metricsSnapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL+"/metrics", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("/metrics returned %d", resp.StatusCode)
	}
	parser := expfmt.NewTextParser(nameValidationScheme)
	families, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		return nil, err
	}
	return &metricsSnapshot{families: families}, nil
}

// counterDelta sums the counter increase for every label-set of the
// given metric family between baseline and final.
func counterDelta(baseline, final *metricsSnapshot, name string) float64 {
	bSum := sumCounter(baseline, name)
	fSum := sumCounter(final, name)
	return fSum - bSum
}

func sumCounter(snap *metricsSnapshot, name string) float64 {
	f, ok := snap.families[name]
	if !ok {
		return 0
	}
	total := 0.0
	for _, m := range f.Metric {
		switch {
		case m.Counter != nil:
			total += m.Counter.GetValue()
		case m.Gauge != nil:
			total += m.Gauge.GetValue()
		}
	}
	return total
}

// poolGaugeByBackend pulls the named db_pool gauge for the given backend
// label. Returns 0 if the gauge isn't present in the snapshot.
func poolGaugeByBackend(snap *metricsSnapshot, name, backend string) float64 {
	fam, ok := snap.families[name]
	if !ok {
		return 0
	}
	for _, m := range fam.Metric {
		for _, l := range m.Label {
			if l.GetName() == "backend" && l.GetValue() == backend && m.Gauge != nil {
				return m.Gauge.GetValue()
			}
		}
	}
	return 0
}

// poolCounterByBackend pulls the named db_pool counter for the given
// backend label, optionally as a delta vs baseline.
func poolCounterByBackend(baseline, final *metricsSnapshot, name, backend string) float64 {
	final0 := readBackendCounter(final, name, backend)
	base0 := readBackendCounter(baseline, name, backend)
	return final0 - base0
}

func readBackendCounter(snap *metricsSnapshot, name, backend string) float64 {
	fam, ok := snap.families[name]
	if !ok {
		return 0
	}
	for _, m := range fam.Metric {
		matched := false
		for _, l := range m.Label {
			if l.GetName() == "backend" && l.GetValue() == backend {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		switch {
		case m.Counter != nil:
			return m.Counter.GetValue()
		case m.Gauge != nil:
			return m.Gauge.GetValue()
		}
	}
	return 0
}

func buildReport(opts runOpts, elapsed time.Duration, baseline, final *metricsSnapshot) string {
	executions := counterDelta(baseline, final, "solidping_check_executions_total")
	rateLimited := counterDelta(baseline, final, "solidping_checks_rate_limited_total")
	busyRetries := counterDelta(baseline, final, "solidping_db_busy_retries_total")

	checksPerMin := 0.0
	if elapsed.Minutes() > 0 {
		checksPerMin = executions / elapsed.Minutes()
	}

	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	w("# SolidPing loadgen report\n\n")
	w("- Backend: `%s`\n", opts.backend)
	w("- Created checks: %d (period %s)\n", opts.nChecks, opts.period)
	w("- Target latency: %s\n", opts.targetLatency)
	w("- Elapsed: %s\n", elapsed.Truncate(time.Second))
	w("- Generated: %s\n\n", time.Now().Format(time.RFC3339))

	w("## Headline\n\n")
	w("| Metric | Value |\n|---|---|\n")
	w("| Total check executions | %.0f |\n", executions)
	w("| Achieved checks/min | **%.1f** |\n", checksPerMin)
	w("| Rate-limited executions | %.0f |\n", rateLimited)
	w("| DB busy retries (delta) | %.0f |\n\n", busyRetries)

	w("## Check stage timings (samples observed during the run)\n\n")
	w("| Stage | Count | p50 | p95 | p99 |\n|---|---:|---:|---:|---:|\n")
	stages := []string{"fetch", "execute", "save_result", "process_incident", "release_lease"}
	for _, stage := range stages {
		count := stageHistogramCount(baseline, final, stage)
		if count == 0 {
			continue
		}
		p50 := stageHistogramQuantile(baseline, final, stage, 0.5)
		p95 := stageHistogramQuantile(baseline, final, stage, 0.95)
		p99 := stageHistogramQuantile(baseline, final, stage, 0.99)
		w("| %s | %.0f | %s | %s | %s |\n", stage, count, fmtSec(p50), fmtSec(p95), fmtSec(p99))
	}
	w("\n")

	w("## DB query timings\n\n")
	w("| Backend | Count | p50 | p95 | p99 |\n|---|---:|---:|---:|---:|\n")
	for _, backend := range []string{"sqlite", "postgres"} {
		count := dbQueryCountByBackend(baseline, final, backend)
		if count == 0 {
			continue
		}
		p50 := dbQueryQuantileByBackend(baseline, final, backend, 0.5)
		p95 := dbQueryQuantileByBackend(baseline, final, backend, 0.95)
		p99 := dbQueryQuantileByBackend(baseline, final, backend, 0.99)
		w("| %s | %.0f | %s | %s | %s |\n", backend, count, fmtSec(p50), fmtSec(p95), fmtSec(p99))
	}
	w("\n")

	w("## DB pool snapshot (final)\n\n")
	w("| Backend | Max open | Open | In use | Idle | Wait count (delta) | Wait duration (delta) |\n")
	w("|---|---:|---:|---:|---:|---:|---:|\n")
	for _, backend := range []string{"sqlite", "postgres"} {
		maxOpen := poolGaugeByBackend(final, "solidping_db_pool_max_open_connections", backend)
		open := poolGaugeByBackend(final, "solidping_db_pool_open_connections", backend)
		inUse := poolGaugeByBackend(final, "solidping_db_pool_in_use_connections", backend)
		idle := poolGaugeByBackend(final, "solidping_db_pool_idle_connections", backend)
		if maxOpen == 0 && open == 0 && inUse == 0 && idle == 0 {
			continue
		}
		wait := poolCounterByBackend(baseline, final, "solidping_db_pool_wait_count_total", backend)
		waitDur := poolCounterByBackend(baseline, final, "solidping_db_pool_wait_duration_seconds_total", backend)
		w(
			"| %s | %.0f | %.0f | %.0f | %.0f | %.0f | %s |\n",
			backend, maxOpen, open, inUse, idle, wait, fmtSec(waitDur),
		)
	}
	w("\n")

	w("## HTTP request timings (selected routes)\n\n")
	w("| Route | Count | p50 | p95 | p99 |\n|---|---:|---:|---:|---:|\n")
	// The /api/v1/workers/* HTTP routes were removed with spec 2026-07-16-02
	// (the in-process worker never used them; deported agents use the
	// WebSocket transport, whose long-lived connection has no per-request
	// histogram to report here).
	for _, route := range []string{
		"/api/v1/orgs/:org/checks",
	} {
		count := routeHistogramCount(baseline, final, route)
		if count == 0 {
			continue
		}
		p50 := routeHistogramQuantile(baseline, final, route, 0.5)
		p95 := routeHistogramQuantile(baseline, final, route, 0.95)
		p99 := routeHistogramQuantile(baseline, final, route, 0.99)
		w("| `%s` | %.0f | %s | %s | %s |\n", route, count, fmtSec(p50), fmtSec(p95), fmtSec(p99))
	}
	w("\n")

	w("## Claim-jobs outcomes\n\n")
	w("| Outcome | Count |\n|---|---:|\n")
	for _, outcome := range []string{"jobs", "empty", "lock_conflict", "error"} {
		c := outcomeCounter(baseline, final, "solidping_claim_jobs_result_total", outcome)
		w("| %s | %.0f |\n", outcome, c)
	}
	w("\n")

	w("## Notes\n\n")
	w("- All p50/p95/p99 figures are estimated by linear interpolation across Prometheus histogram buckets;\n")
	w("  for finer-grained percentiles run PromQL `histogram_quantile()` against the live /metrics endpoint.\n")
	w("- Counter values are deltas measured between baseline and final scrapes.\n")
	w("- DB pool gauges are final-snapshot values; a non-zero `Wait duration` here is the strongest signal that\n")
	w("  the pool is the bottleneck.\n")

	return b.String()
}

func fmtSec(seconds float64) string {
	if seconds <= 0 {
		return "—"
	}
	switch {
	case seconds < 0.001:
		return fmt.Sprintf("%.0fµs", seconds*1e6)
	case seconds < 1:
		return fmt.Sprintf("%.1fms", seconds*1e3)
	default:
		return fmt.Sprintf("%.2fs", seconds)
	}
}

// stageHistogramCount counts samples observed during the run for the
// given check-stage label.
func stageHistogramCount(baseline, final *metricsSnapshot, stage string) float64 {
	return labelHistogramCount(baseline, final, "solidping_check_stage_duration_seconds", "stage", stage)
}

func stageHistogramQuantile(baseline, final *metricsSnapshot, stage string, q float64) float64 {
	return labelHistogramQuantile(baseline, final, "solidping_check_stage_duration_seconds", "stage", stage, q)
}

func dbQueryCountByBackend(baseline, final *metricsSnapshot, backend string) float64 {
	return labelHistogramCount(baseline, final, "solidping_db_query_duration_seconds", "backend", backend)
}

func dbQueryQuantileByBackend(baseline, final *metricsSnapshot, backend string, q float64) float64 {
	return labelHistogramQuantile(baseline, final, "solidping_db_query_duration_seconds", "backend", backend, q)
}

func routeHistogramCount(baseline, final *metricsSnapshot, route string) float64 {
	return labelHistogramCount(baseline, final, "solidping_http_request_duration_seconds", "route", route)
}

func routeHistogramQuantile(baseline, final *metricsSnapshot, route string, q float64) float64 {
	return labelHistogramQuantile(baseline, final, "solidping_http_request_duration_seconds", "route", route, q)
}

// labelHistogramCount sums sample counts where the given label has the
// given value, across all other labels.
func labelHistogramCount(baseline, final *metricsSnapshot, family, label, value string) float64 {
	bSum := readLabeledHistogramSamples(baseline, family, label, value)
	fSum := readLabeledHistogramSamples(final, family, label, value)
	return fSum - bSum
}

func readLabeledHistogramSamples(snap *metricsSnapshot, family, label, value string) float64 {
	fam, ok := snap.families[family]
	if !ok {
		return 0
	}
	total := 0.0
	for _, m := range fam.Metric {
		if !labelMatches(m, label, value) || m.Histogram == nil {
			continue
		}
		total += float64(m.Histogram.GetSampleCount())
	}
	return total
}

func labelMatches(m *dto.Metric, label, value string) bool {
	for _, l := range m.Label {
		if l.GetName() == label && l.GetValue() == value {
			return true
		}
	}
	return false
}

// labelHistogramQuantile estimates the quantile for the subset of a
// histogram family where the given label has the given value. Same
// linear-interpolation approach as histogramQuantile but pre-filters
// the samples by label match.
func labelHistogramQuantile(baseline, final *metricsSnapshot, family, label, value string, q float64) float64 {
	finalFam, ok := final.families[family]
	if !ok {
		return 0
	}
	baselineFam := baseline.families[family]
	byBoundFinal := map[float64]float64{}
	byBoundBase := map[float64]float64{}
	lastFinite := 0.0
	collect := func(fam *dto.MetricFamily, dst map[float64]float64) {
		if fam == nil {
			return
		}
		for _, m := range fam.Metric {
			if !labelMatches(m, label, value) || m.Histogram == nil {
				continue
			}
			for _, b := range m.Histogram.Bucket {
				bound := b.GetUpperBound()
				dst[bound] += float64(b.GetCumulativeCount())
				if !math.IsInf(bound, +1) && bound > lastFinite {
					lastFinite = bound
				}
			}
		}
	}
	collect(finalFam, byBoundFinal)
	collect(baselineFam, byBoundBase)
	bounds := make([]float64, 0, len(byBoundFinal))
	for b := range byBoundFinal {
		bounds = append(bounds, b)
	}
	sort.Float64s(bounds)
	total := byBoundFinal[math.Inf(+1)] - byBoundBase[math.Inf(+1)]
	if total <= 0 {
		return 0
	}
	target := q * total
	prevBound := 0.0
	prevCount := 0.0
	for _, b := range bounds {
		c := byBoundFinal[b] - byBoundBase[b]
		if c >= target {
			if math.IsInf(b, +1) {
				return lastFinite
			}
			span := c - prevCount
			if span <= 0 {
				return b
			}
			ratio := (target - prevCount) / span
			return prevBound + ratio*(b-prevBound)
		}
		prevBound = b
		prevCount = c
	}
	return lastFinite
}

// outcomeCounter sums the counter delta where the given label matches.
func outcomeCounter(baseline, final *metricsSnapshot, family, outcome string) float64 {
	val := func(snap *metricsSnapshot) float64 {
		fam, ok := snap.families[family]
		if !ok {
			return 0
		}
		for _, m := range fam.Metric {
			if labelMatches(m, "outcome", outcome) && m.Counter != nil {
				return m.Counter.GetValue()
			}
		}
		return 0
	}
	return val(final) - val(baseline)
}
