// Package checkntp provides active Network Time Protocol (NTP) checks: it
// queries a time server and judges it as a clock — reachability plus the
// server's own self-reported health (stratum, leap indicator, root distance)
// via beevik/ntp's Response.Validate(), with optional clock-offset and
// max-stratum thresholds layered on top.
package checkntp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/beevik/ntp"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	microsecondsPerMilli = 1000.0
	secondsPerNano       = 1e9
)

// QueryFunc is the signature of the NTP query call the checker depends on. It
// mirrors ntp.QueryWithOptions so production wires the real client while tests
// feed synthetic *ntp.Response values without touching the network.
type QueryFunc func(address string, opt ntp.QueryOptions) (*ntp.Response, error)

// defaultQueryFunc is the production query implementation.
//
//nolint:gochecknoglobals // package-level seam; mirrors checkfreeboxline indirection.
var defaultQueryFunc QueryFunc = ntp.QueryWithOptions

// queryCtxKeyType makes the context-key unique within the binary.
type queryCtxKeyType struct{}

//nolint:gochecknoglobals // sentinel value for context key
var queryCtxKey = queryCtxKeyType{}

// WithQueryFunc returns a context carrying a query function that overrides the
// default ntp.QueryWithOptions. Intended for tests so parallel runs don't race
// over a single global.
func WithQueryFunc(ctx context.Context, queryFunc QueryFunc) context.Context {
	return context.WithValue(ctx, queryCtxKey, queryFunc)
}

// queryFromContext returns the per-context query func if set, else the default.
func queryFromContext(ctx context.Context) QueryFunc {
	if v, ok := ctx.Value(queryCtxKey).(QueryFunc); ok && v != nil {
		return v
	}

	return defaultQueryFunc
}

// NTPChecker implements the Checker interface for NTP time-source checks.
type NTPChecker struct{}

// Type returns the check type identifier.
func (c *NTPChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeNTP
}

// Validate checks if the configuration is valid (no network operations).
func (c *NTPChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &NTPConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Name == "" {
		spec.Name = "NTP: " + cfg.Host
	}

	if spec.Slug == "" {
		spec.Slug = "ntp-" + sanitizeSlug(cfg.Host)
	}

	return nil
}

// sanitizeSlug turns a host into a slug-friendly fragment.
func sanitizeSlug(host string) string {
	out := make([]rune, 0, len(host))

	for _, r := range host {
		switch r {
		case '.', ':':
			out = append(out, '-')
		default:
			out = append(out, r)
		}
	}

	return string(out)
}

// Execute performs the NTP check and returns the result.
func (c *NTPChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*NTPConfig](config)
	if err != nil {
		return nil, err
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	port := cfg.Port
	if port == 0 {
		port = defaultPort
	}

	version := cfg.Version
	if version == 0 {
		version = defaultVersion
	}

	start := time.Now()

	address := net.JoinHostPort(cfg.Host, strconv.Itoa(port))

	resp, err := queryFromContext(ctx)(address, ntp.QueryOptions{
		Timeout: timeout,
		Version: version,
	})
	if err != nil {
		return queryErrorResult(ctx, cfg, port, start, err), nil
	}

	// Defensive: the library never returns (nil, nil), but a nil response must
	// surface as Down rather than panicking on a field access in classify.
	if resp == nil {
		return queryErrorResult(ctx, cfg, port, start, errNilResponse), nil
	}

	return classify(cfg, port, start, resp), nil
}

// errNilResponse guards the (nil, nil) query return that the real client never
// produces but a faulty stub could.
var errNilResponse = errors.New("nil NTP response")

// queryErrorResult maps a transport/query error to Timeout (when the context
// deadline fired) or Down (any other failure: refused, unreachable, malformed).
func queryErrorResult(
	ctx context.Context, cfg *NTPConfig, port int, start time.Time, err error,
) *checkerdef.Result {
	status := checkerdef.StatusDown
	if ctx.Err() != nil {
		status = checkerdef.StatusTimeout
	}

	return &checkerdef.Result{
		Status:   status,
		Duration: time.Since(start),
		Metrics:  map[string]any{},
		Output: map[string]any{
			checkerdef.OutputKeyHost:  cfg.Host,
			checkerdef.OutputKeyPort:  port,
			checkerdef.OutputKeyError: fmt.Sprintf("NTP query failed: %v", err),
		},
	}
}

// classify turns a successful NTP response into a check result, applying the
// default health verdict (Validate) and the optional offset/stratum thresholds.
func classify(cfg *NTPConfig, port int, start time.Time, resp *ntp.Response) *checkerdef.Result {
	duration := resp.RTT
	if duration <= 0 {
		duration = time.Since(start)
	}

	metrics := buildMetrics(resp)
	output := buildOutput(cfg, port, resp)

	// Default health: the server's own self-report. Validate() returns an error
	// for stratum 0 (Kiss-o'-Death), stratum 16 (unsynchronized), LeapNotInSync,
	// and an out-of-range root distance — none of which trust the worker clock.
	if err := resp.Validate(); err != nil {
		output[checkerdef.OutputKeyError] = "NTP server unhealthy: " + err.Error()
		if resp.IsKissOfDeath() {
			output["kiss_code"] = resp.KissCode
		}

		return &checkerdef.Result{Status: checkerdef.StatusDown, Duration: duration, Metrics: metrics, Output: output}
	}

	if status, msg := evaluateThresholds(cfg, resp); status != checkerdef.StatusUp {
		output[checkerdef.OutputKeyError] = msg

		return &checkerdef.Result{Status: status, Duration: duration, Metrics: metrics, Output: output}
	}

	return &checkerdef.Result{Status: checkerdef.StatusUp, Duration: duration, Metrics: metrics, Output: output}
}

// evaluateThresholds applies the opt-in offset/stratum thresholds in
// severity order (Down before Warning). Returns StatusUp and "" when every
// configured threshold passes.
func evaluateThresholds(cfg *NTPConfig, resp *ntp.Response) (checkerdef.Status, string) {
	offsetMs := durationToMs(resp.ClockOffset)
	absOffsetMs := offsetMs
	if absOffsetMs < 0 {
		absOffsetMs = -absOffsetMs
	}

	if cfg.MaxStratum > 0 && int(resp.Stratum) > cfg.MaxStratum {
		return checkerdef.StatusDown, fmt.Sprintf(
			"stratum %d above threshold %d", resp.Stratum, cfg.MaxStratum,
		)
	}

	if cfg.OffsetCritMs > 0 && absOffsetMs > float64(cfg.OffsetCritMs) {
		return checkerdef.StatusDown, fmt.Sprintf(
			"clock offset %.1fms exceeds critical threshold %dms", offsetMs, cfg.OffsetCritMs,
		)
	}

	if cfg.OffsetWarnMs > 0 && absOffsetMs > float64(cfg.OffsetWarnMs) {
		return checkerdef.StatusWarning, fmt.Sprintf(
			"clock offset %.1fms exceeds warning threshold %dms", offsetMs, cfg.OffsetWarnMs,
		)
	}

	return checkerdef.StatusUp, ""
}

// buildMetrics returns the aggregatable numeric readings. Names are unsuffixed
// and fall to the type-based aggregation defaults (as checkssl's days_remaining
// and checkudp's bytes_sent already do).
func buildMetrics(resp *ntp.Response) map[string]any {
	return map[string]any{
		"offset_ms":          durationToMs(resp.ClockOffset),
		"rtt_ms":             durationToMs(resp.RTT),
		"stratum":            int(resp.Stratum),
		"root_delay_ms":      durationToMs(resp.RootDelay),
		"root_dispersion_ms": durationToMs(resp.RootDispersion),
		"root_distance_ms":   durationToMs(resp.RootDistance),
		"poll_s":             durationToSeconds(resp.Poll),
		"precision_ms":       durationToMs(resp.Precision),
	}
}

// buildOutput returns the human-facing diagnostics map.
func buildOutput(cfg *NTPConfig, port int, resp *ntp.Response) map[string]any {
	output := map[string]any{
		checkerdef.OutputKeyHost: cfg.Host,
		checkerdef.OutputKeyPort: port,
		"stratum":                int(resp.Stratum),
		"leap":                   leapString(resp.Leap),
		"reference_id":           resp.ReferenceID,
		"offset":                 humanOffset(resp.ClockOffset),
	}

	if !resp.Time.IsZero() {
		output["server_time"] = resp.Time.UTC().Format(time.RFC3339)
	}

	return output
}

// durationToMs converts a duration to milliseconds as a float.
func durationToMs(d time.Duration) float64 {
	return float64(d.Microseconds()) / microsecondsPerMilli
}

// durationToSeconds converts a duration to seconds as a float.
func durationToSeconds(d time.Duration) float64 {
	return float64(d.Nanoseconds()) / secondsPerNano
}

// humanOffset renders a signed offset like "+12ms" / "-3ms".
func humanOffset(d time.Duration) string {
	ms := durationToMs(d)
	if ms >= 0 {
		return fmt.Sprintf("+%.0fms", ms)
	}

	return fmt.Sprintf("%.0fms", ms)
}

// leapString renders the leap indicator as a stable string.
func leapString(leap ntp.LeapIndicator) string {
	switch leap {
	case ntp.LeapNoWarning:
		return "no_warning"
	case ntp.LeapAddSecond:
		return "add_second"
	case ntp.LeapDelSecond:
		return "del_second"
	case ntp.LeapNotInSync:
		return "not_in_sync"
	default:
		return "unknown"
	}
}

// Ensure NTPChecker satisfies the Checker interface.
var _ checkerdef.Checker = (*NTPChecker)(nil)
