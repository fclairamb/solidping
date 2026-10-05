// Package checkcrawl implements the `crawl` check type (spec 2026-10-03-03):
// a website crawl for broken internal and external links, mixed content and
// sitemap errors. It is the first multi-step checker: it runs as a series of
// bounded slices whose state the worker persists between them.
package checkcrawl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	crawlconfig "github.com/fclairamb/solidping/server/internal/checkers/checkcrawl/config"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// UnitsPerSlice is the per-slice work-unit cap (spec §1.5: crawl = 20).
const UnitsPerSlice = 20

// Output limits (spec §2.5).
const (
	outputFindings    = 50
	outputNewFindings = 20
	errorSummaryItems = 3
)

// Output keys.
const (
	OutputKeyCounts        = "counts"
	OutputKeyFindings      = "findings"
	OutputKeyFindingsTotal = "findingsTotal"
	OutputKeyNewFindings   = "newFindings"
	OutputKeyPagesCrawled  = "pagesCrawled"
	OutputKeyIncomplete    = "incomplete"
)

var (
	errNotCrawlConfig = errors.New("invalid configuration type")
	errCorruptState   = errors.New("corrupt crawl state")
)

// CrawlChecker implements checkerdef.StepChecker for website crawls.
type CrawlChecker struct {
	// now is the clock (tests).
	now func() time.Time
	// transport overrides the egress-guarded transport (tests only).
	transport http.RoundTripper
}

// Type returns the check type identifier.
func (c *CrawlChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeCrawl
}

// Validate validates the configuration offline.
func (c *CrawlChecker) Validate(spec *checkerdef.CheckSpec) error {
	return crawlconfig.ValidateSpec(spec)
}

// UnitsPerSlice implements checkerdef.StepChecker.
func (c *CrawlChecker) UnitsPerSlice() int {
	return UnitsPerSlice
}

func (c *CrawlChecker) clock() time.Time {
	if c.now != nil {
		return c.now()
	}

	return time.Now()
}

// Execute runs a whole crawl in one go, within ctx's deadline. The worker
// never calls it for a multi-step type (it calls Step); it serves one-shot
// callers such as a test run.
func (c *CrawlChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, ok := config.(*CrawlConfig)
	if !ok {
		return nil, errNotCrawlConfig
	}

	const oneShotBudget = time.Minute

	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = c.clock().Add(oneShotBudget)
	}

	var state []byte

	for {
		remaining := time.Until(deadline)
		out, err := c.Step(ctx, config, checkerdef.StepInput{
			State:    state,
			Budget:   remaining,
			MaxUnits: cfg.EffectiveMaxPages() + maxExternalLinks + maxSitemapFetches + 1,
			Final:    remaining <= minSliceRemaining,
		})
		if err != nil {
			return nil, err
		}

		if out.Done {
			return out.Result, nil
		}

		state = out.State
	}
}

// Step implements checkerdef.StepChecker: it loads the state, crawls until
// the slice budget or the units run out, and either returns the next state
// or, when the queue is empty or in.Final is set, the run's result.
func (c *CrawlChecker) Step(
	ctx context.Context, config checkerdef.Config, input checkerdef.StepInput,
) (checkerdef.StepOutput, error) {
	cfg, ok := config.(*CrawlConfig)
	if !ok {
		return checkerdef.StepOutput{}, errNotCrawlConfig
	}

	state, err := c.loadState(cfg, input.State)
	if err != nil {
		return checkerdef.StepOutput{}, err
	}

	state.Slices++

	transport := c.transport
	if transport == nil {
		transport = checkerdef.HTTPTransportFor(ctx, false)
	}

	run, err := newRunner(cfg, state, &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, input.MaxUnits)
	if err != nil {
		return checkerdef.StepOutput{}, checkerdef.NewConfigError("url", err.Error())
	}

	complete := false

	if !input.Final {
		sliceCtx, cancel := context.WithTimeout(ctx, input.Budget)
		complete = run.runSlice(sliceCtx)

		cancel()
	}

	state.Seen = encodeSeen(run.seen)

	if complete || input.Final {
		return c.finish(cfg, state, run.used, !complete)
	}

	encoded, err := json.Marshal(state)
	if err != nil {
		return checkerdef.StepOutput{}, fmt.Errorf("encode crawl state: %w", err)
	}

	return checkerdef.StepOutput{State: encoded, Units: run.used, Progress: progress(cfg, state)}, nil
}

func (c *CrawlChecker) loadState(cfg *CrawlConfig, raw []byte) (*crawlState, error) {
	if len(raw) == 0 {
		return initialState(cfg, c.clock()), nil
	}

	var state crawlState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("%w: %w", errCorruptState, err)
	}

	return &state, nil
}

func progress(cfg *CrawlConfig, state *crawlState) map[string]any {
	return map[string]any{
		"pagesDone": state.Pages,
		"maxPages":  cfg.EffectiveMaxPages(),
		"queued":    len(state.Queue),
		"findings":  len(state.Findings),
		"phase":     state.Phase,
	}
}

// finish builds the run's result and full report.
func (c *CrawlChecker) finish(
	cfg *CrawlConfig, state *crawlState, used int, incomplete bool,
) (checkerdef.StepOutput, error) {
	findings := state.Findings
	if findings == nil {
		findings = []Finding{} // a clean crawl reports an empty list, never null
	}

	report, err := json.Marshal(Report{Findings: findings, PagesCrawled: state.Pages, Incomplete: incomplete})
	if err != nil {
		return checkerdef.StepOutput{}, fmt.Errorf("encode crawl report: %w", err)
	}

	duration := c.clock().Sub(time.UnixMilli(state.StartedAtMs))
	result := buildResult(cfg, state, incomplete, duration)

	// With no previous report every finding is new; the worker re-diffs
	// against the previous run's report through DiffAgainst.
	setNewFindings(result, state.Findings, nil)

	return checkerdef.StepOutput{
		Done:     true,
		Result:   result,
		Report:   report,
		Units:    used,
		Progress: progress(cfg, state),
	}, nil
}

// Report is the full crawl report stored as the `crawl-report` attachment.
type Report struct {
	Findings     []Finding `json:"findings"`
	PagesCrawled int       `json:"pagesCrawled"`
	Incomplete   bool      `json:"incomplete"`
}

func buildResult(cfg *CrawlConfig, state *crawlState, incomplete bool, duration time.Duration) *checkerdef.Result {
	counts := map[string]any{}
	countOf := map[string]int{}

	for _, findingType := range crawlconfig.FindingTypes() {
		countOf[findingType] = 0
	}

	for i := range state.Findings {
		countOf[state.Findings[i].Type]++
	}

	for findingType, count := range countOf {
		counts[findingType] = count
	}

	firstFindings := state.Findings[:min(outputFindings, len(state.Findings))]

	return &checkerdef.Result{
		Status:   resultStatus(cfg, countOf, len(state.Findings), incomplete),
		Duration: duration,
		Metrics: map[string]any{
			"pages_crawled":         state.Pages,
			"broken_links":          countOf[findingBrokenLink],
			"broken_external_links": countOf[findingBrokenExternalLink],
			"mixed_content":         countOf[findingMixedContentActive] + countOf[findingMixedContentPassive],
			"sitemap_errors":        countOf[findingSitemapError],
			"run_duration_ms":       duration.Milliseconds(),
			"slices":                state.Slices,
		},
		Output: map[string]any{
			checkerdef.OutputKeyURL: cfg.URL,
			OutputKeyCounts:         counts,
			OutputKeyFindings:       findingsToAny(firstFindings),
			OutputKeyFindingsTotal:  len(state.Findings),
			OutputKeyPagesCrawled:   state.Pages,
			OutputKeyIncomplete:     incomplete,
		},
	}
}

// resultStatus applies spec §2.5: down on any failOn finding; warning on
// other findings or an incomplete run; up otherwise.
func resultStatus(cfg *CrawlConfig, countOf map[string]int, total int, incomplete bool) checkerdef.Status {
	for findingType := range failOnSet(cfg) {
		if countOf[findingType] > 0 {
			return checkerdef.StatusDown
		}
	}

	if total > 0 || incomplete {
		return checkerdef.StatusWarning
	}

	return checkerdef.StatusUp
}

// DiffAgainst implements checkerdef.ReportDiffer: it recomputes the
// newFindings output of a finished run against the previous run's report.
// A nil or unreadable previous report means every finding is new.
func (c *CrawlChecker) DiffAgainst(previous []byte, out *checkerdef.StepOutput) {
	if out == nil || out.Result == nil || len(out.Report) == 0 {
		return
	}

	var current Report
	if err := json.Unmarshal(out.Report, &current); err != nil {
		return
	}

	var prevFingerprints map[string]bool

	if len(previous) > 0 {
		var prev Report
		if err := json.Unmarshal(previous, &prev); err == nil {
			prevFingerprints = make(map[string]bool, len(prev.Findings))
			for i := range prev.Findings {
				prevFingerprints[prev.Findings[i].Fingerprint] = true
			}
		}
	}

	setNewFindings(out.Result, current.Findings, prevFingerprints)
}

// setNewFindings writes newFindings (count + first 20) and, when the run is
// down, an error line naming the new findings first: that line is what an
// incident notification shows.
func setNewFindings(result *checkerdef.Result, findings []Finding, previous map[string]bool) {
	var fresh []Finding

	for i := range findings {
		if !previous[findings[i].Fingerprint] {
			fresh = append(fresh, findings[i])
		}
	}

	result.Output[OutputKeyNewFindings] = map[string]any{
		"count": len(fresh),
		"items": findingsToAny(fresh[:min(outputNewFindings, len(fresh))]),
	}

	if result.Status != checkerdef.StatusDown {
		delete(result.Output, checkerdef.OutputKeyError)

		return
	}

	result.Output[checkerdef.OutputKeyError] = errorSummary(findings, fresh)
}

func errorSummary(all, fresh []Finding) string {
	list := fresh
	label := "new"

	if len(list) == 0 {
		list = all
		label = "total"
	}

	parts := make([]string, 0, errorSummaryItems)
	for i := range min(errorSummaryItems, len(list)) {
		parts = append(parts, list[i].Type+" "+list[i].URL)
	}

	summary := fmt.Sprintf("%d %s finding(s): %s", len(list), label, strings.Join(parts, ", "))
	if len(list) > errorSummaryItems {
		summary += ", …"
	}

	return summary
}

func findingsToAny(findings []Finding) []any {
	out := make([]any, 0, len(findings))

	for i := range findings {
		finding := &findings[i]
		item := map[string]any{
			"type":        finding.Type,
			"url":         finding.URL,
			"fingerprint": finding.Fingerprint,
		}

		if finding.Source != "" {
			item["source"] = finding.Source
		}

		if finding.Status != 0 {
			item["status"] = finding.Status
		}

		if finding.Error != "" {
			item["error"] = finding.Error
		}

		out = append(out, item)
	}

	return out
}
