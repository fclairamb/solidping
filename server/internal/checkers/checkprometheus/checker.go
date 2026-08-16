package checkprometheus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/version"
)

// Output keys specific to this checker.
const (
	outputKeyMode          = "mode"
	outputKeyMetric        = "metric"
	outputKeyQuery         = "query"
	outputKeyValue         = "value"
	outputKeyLabels        = "labels"
	outputKeyMatchedCount  = "matched_count"
	outputKeyMatchedLabels = "matched_labels"
	outputKeyOperator      = "operator"
	outputKeyThreshold     = "threshold"
	outputKeyTier          = "tier"
	outputKeyMessage       = "message"

	// tier values written to outputKeyTier.
	tierCritical = "critical"
	tierWarning  = "warning"
	tierOK       = "ok"

	// MetricKeyValue is the result metric name. It is deliberately
	// unsuffixed: the aggregation job's suffix convention falls through to
	// the type-based default for a float64, which is "average" — so the
	// monitored value rolls up and graphs over time exactly like a latency.
	MetricKeyValue = "value"
)

// PrometheusChecker implements the Checker interface for Prometheus metric
// checks.
type PrometheusChecker struct{}

// Type returns the check type identifier.
func (c *PrometheusChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypePrometheus
}

// Validate checks the configuration and fills in a default name/slug.
func (c *PrometheusChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &PrometheusConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	subject := cfg.Metric
	if cfg.EffectiveMode() == ModePromQL {
		subject = cfg.Query
	}

	if spec.Name == "" {
		spec.Name = "Prometheus: " + subject
	}

	// A default slug is part of "done" for a check type — leaving it empty
	// regresses the checkdnsbl/checksip fix.
	if spec.Slug == "" {
		spec.Slug = defaultSlug(cfg.URL)
	}

	return nil
}

// defaultSlug derives `prometheus-<host>` from the target URL, falling back to
// a bare `prometheus` when the host cannot be read.
func defaultSlug(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return "prometheus"
	}

	host := strings.NewReplacer(".", "-", ":", "-").Replace(parsed.Hostname())

	return "prometheus-" + host
}

// Execute performs the check and grades the resolved value.
func (c *PrometheusChecker) Execute(
	ctx context.Context, config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*PrometheusConfig](config)
	if err != nil {
		return nil, err
	}

	start := time.Now()

	resolved, fetchErr := fetchValue(ctx, cfg)
	duration := time.Since(start)

	if fetchErr != nil {
		return fetchFailureResult(cfg, duration, fetchErr), nil
	}

	if resolved.Matched == 0 {
		return missingResult(cfg, duration), nil
	}

	return gradeResult(cfg, resolved, duration), nil
}

// resolution is the outcome of one fetch: the single numeric value, how many
// series the selector matched, and a rendering of the matched labels for the
// result output. Matched == 0 means "nothing matched" and onMissing applies.
type resolution struct {
	Value   float64
	Matched int
	Labels  string
}

// fetchValue resolves the single numeric value for this execution.
func fetchValue(ctx context.Context, cfg *PrometheusConfig) (*resolution, error) {
	if cfg.EffectiveMode() == ModePromQL {
		return fetchPromQL(ctx, cfg)
	}

	return fetchScrape(ctx, cfg)
}

// newClient builds the HTTP client. The transport comes from the shared
// checkerdef helper, so a `tunnelCheckUid` (SSH port-forward) and a pinned
// `ipVersion` are honored here exactly as they are for the HTTP check — no
// bespoke dialing path in this package.
func newClient(ctx context.Context, cfg *PrometheusConfig) *http.Client {
	return &http.Client{
		Timeout: cfg.EffectiveTimeout(),
		Transport: checkerdef.BuildHTTPTransport(
			checkerdef.TunnelDialerFrom(ctx), false, checkerdef.IPVersionFrom(ctx),
		),
	}
}

// doGet issues the GET with the configured headers applied.
func doGet(ctx context.Context, cfg *PrometheusConfig, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Only override the User-Agent when the binary actually carries one:
	// Header.Set with an empty value SUPPRESSES the header entirely, which
	// would make our probes anonymous rather than identified.
	if version.UserAgent != "" {
		req.Header.Set("User-Agent", version.UserAgent)
	}

	for key, value := range cfg.Headers {
		req.Header.Set(key, value)
	}

	resp, err := newClient(ctx, cfg).Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return resp, nil
}

var (
	errNon200     = errors.New("unexpected HTTP status")
	errBodyTooBig = errors.New("metrics response too large")
	errAmbiguous  = errors.New("selector matched multiple series")
)

// fetchScrape scrapes the endpoint and resolves the configured selector.
func fetchScrape(ctx context.Context, cfg *PrometheusConfig) (*resolution, error) {
	resp, err := doGet(ctx, cfg, cfg.URL)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %d", errNon200, resp.StatusCode)
	}

	body, err := readCapped(resp.Body, capHintScrape)
	if err != nil {
		return nil, err
	}

	// The parser must be constructed with an explicit name-validation scheme
	// — the zero-value TextParser panics. UTF8 is the modern default and the
	// permissive one, so a target exposing UTF-8 metric names is read rather
	// than rejected.
	parser := expfmt.NewTextParser(model.UTF8Validation)

	families, err := parser.TextToMetricFamilies(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to parse metrics: %w", err)
	}

	matched := selectSeries(flattenFamilies(families), cfg.Metric, cfg.Labels)
	if len(matched) == 0 {
		return &resolution{Matched: 0}, nil
	}

	return resolveMatched(cfg, matched)
}

// readCapped reads at most MaxScrapeBytes, and REFUSES a longer body instead of
// truncating it. Truncation is the dangerous option: a half-read exposition
// body still parses, and the check would then grade a wrong value while looking
// perfectly healthy. One byte past the cap is read purely to detect the
// overflow.
func readCapped(body io.Reader, hint string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, MaxScrapeBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	const bytesPerMB = 1024 * 1024

	if len(data) > MaxScrapeBytes {
		return nil, fmt.Errorf(
			"%w: exceeds the %d MB limit; the body was refused, not truncated "+
				"(a truncated exposition body would parse into wrong values) — %s",
			errBodyTooBig, MaxScrapeBytes/bytesPerMB, hint,
		)
	}

	return data, nil
}

// Hints appended to the over-cap error, so the fix names the mode the operator
// is actually in.
const (
	capHintScrape = "narrow the endpoint or switch to promql mode"
	capHintPromQL = "make the query return fewer series"
)

// resolveMatched applies the multi-series strategy to the matched series.
func resolveMatched(cfg *PrometheusConfig, matched []series) (*resolution, error) {
	match := cfg.EffectiveMatch()

	if match == MatchSingle && len(matched) > 1 {
		return nil, fmt.Errorf(
			"%w: %d series matched (%s and %d more); narrow the selector with labels "+
				"or set match to min/max/sum/avg",
			errAmbiguous, len(matched), describeLabels(matched[0].Labels), len(matched)-1,
		)
	}

	value, err := aggregate(matched, match)
	if err != nil {
		return nil, err
	}

	labels := describeLabels(matched[0].Labels)
	if len(matched) > 1 {
		labels = fmt.Sprintf("%s (+%d more)", labels, len(matched)-1)
	}

	return &resolution{Value: value, Matched: len(matched), Labels: labels}, nil
}

// fetchFailureResult renders an unreachable/non-200/parse/ambiguity failure.
func fetchFailureResult(
	cfg *PrometheusConfig, duration time.Duration, err error,
) *checkerdef.Result {
	status := checkerdef.StatusDown
	if errors.Is(err, context.DeadlineExceeded) {
		status = checkerdef.StatusTimeout
	}

	return &checkerdef.Result{
		Status:   status,
		Duration: duration,
		Output:   baseOutput(cfg, map[string]any{checkerdef.OutputKeyError: err.Error()}),
	}
}

// missingResult renders the onMissing outcome.
func missingResult(cfg *PrometheusConfig, duration time.Duration) *checkerdef.Result {
	status := checkerdef.StatusDown

	switch cfg.EffectiveOnMissing() {
	case OnMissingUp:
		status = checkerdef.StatusUp
	case OnMissingWarning:
		status = checkerdef.StatusWarning
	case OnMissingDown:
		status = checkerdef.StatusDown
	}

	subject := cfg.Metric
	if cfg.EffectiveMode() == ModePromQL {
		subject = cfg.Query
	}

	message := fmt.Sprintf(
		"no series matched %q (onMissing: %s)", subject, cfg.EffectiveOnMissing(),
	)

	out := baseOutput(cfg, map[string]any{
		outputKeyMatchedCount: 0,
		outputKeyMessage:      message,
	})

	if status != checkerdef.StatusUp {
		out[checkerdef.OutputKeyError] = message
	}

	return &checkerdef.Result{Status: status, Duration: duration, Output: out}
}

// gradeResult compares the resolved value against the thresholds.
//
// Critical breached → StatusDown (pages). Otherwise warning breached →
// StatusWarning (amber, counts as up, no incident). Otherwise StatusUp. A
// config with only a warningValue is valid and can therefore never page —
// intended, and the same contract as `warningDays` on domain/ssl.
func gradeResult(
	cfg *PrometheusConfig, resolved *resolution, duration time.Duration,
) *checkerdef.Result {
	operator := cfg.EffectiveOperator()
	value := resolved.Value

	status := checkerdef.StatusUp
	tier := tierOK
	message := fmt.Sprintf("%g is within thresholds", value)

	var threshold *float64

	switch {
	case cfg.CriticalValue != nil && breaches(value, operator, *cfg.CriticalValue):
		status = checkerdef.StatusDown
		tier = tierCritical
		threshold = cfg.CriticalValue
	case cfg.WarningValue != nil && breaches(value, operator, *cfg.WarningValue):
		status = checkerdef.StatusWarning
		tier = tierWarning
		threshold = cfg.WarningValue
	}

	if threshold != nil {
		message = fmt.Sprintf("%g %s %s threshold %g", value, operator, tier, *threshold)
	}

	out := baseOutput(cfg, map[string]any{
		outputKeyValue:         value,
		outputKeyMatchedCount:  resolved.Matched,
		outputKeyMatchedLabels: resolved.Labels,
		outputKeyOperator:      operator,
		outputKeyTier:          tier,
		outputKeyMessage:       message,
	})

	if threshold != nil {
		out[outputKeyThreshold] = *threshold
		out[checkerdef.OutputKeyError] = message
	}

	return &checkerdef.Result{
		Status:   status,
		Duration: duration,
		Metrics:  map[string]any{MetricKeyValue: value},
		Output:   out,
	}
}

// breaches reports whether `value <operator> threshold` holds.
func breaches(value float64, operator string, threshold float64) bool {
	switch operator {
	case OpGreater:
		return value > threshold
	case OpGreaterEqual:
		return value >= threshold
	case OpLess:
		return value < threshold
	case OpLessEqual:
		return value <= threshold
	case OpEqual:
		return value == threshold
	case OpNotEqual:
		return value != threshold
	default:
		return false
	}
}

// baseOutput seeds the shared output keys every result carries.
func baseOutput(cfg *PrometheusConfig, extra map[string]any) map[string]any {
	out := map[string]any{
		outputKeyMode:           cfg.EffectiveMode(),
		checkerdef.OutputKeyURL: cfg.URL,
	}

	if cfg.EffectiveMode() == ModePromQL {
		out[outputKeyQuery] = cfg.Query
	} else {
		out[outputKeyMetric] = cfg.Metric

		if len(cfg.Labels) > 0 {
			out[outputKeyLabels] = describeLabels(cfg.Labels)
		}
	}

	for k, v := range extra {
		out[k] = v
	}

	return out
}
