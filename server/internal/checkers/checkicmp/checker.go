// Package checkicmp provides ICMP ping monitoring checks.
package checkicmp

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	// Default values from spec.
	defaultTimeout    = 5 * time.Second
	defaultCount      = 1
	defaultInterval   = 1 * time.Second
	defaultPacketSize = 56

	// Burst limits (spec 2026-09-21-01). The interval floor is 50ms because
	// interval only ever adds delay — lowering it shortens the burst — and the
	// count ceiling of 600 at 50ms spans a 30-second window.
	minCount    = 1
	maxCount    = 600
	minInterval = 50 * time.Millisecond
	maxInterval = 60 * time.Second

	// Network constants.
	percentageMultiplier = 100    // Multiplier for percentage calculations
	microsecondsToMillis = 1000.0 // Conversion factor from microseconds to milliseconds

	// ICMP protocol numbers.
	protocolICMP   = 1
	protocolICMPv6 = 58

	methodICMP = "icmp"

	// Metric keys, named because the failure paths repeat the "nothing was
	// sent" metric block.
	metricPacketsSent     = "packets_sent"
	metricPacketsReceived = "packets_received"
	metricPacketLossPct   = "packet_loss_pct"
	metricRTTJitterMs     = "rtt_ms_jitter"
)

// ICMPChecker implements the Checker interface for ICMP ping checks.
type ICMPChecker struct{}

// Type returns the check type identifier.
func (c *ICMPChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeICMP
}

// Validate checks if the configuration is valid.
//
//nolint:cyclop // Validation requires checking multiple fields
func (c *ICMPChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &ICMPConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	// Validate Host
	if cfg.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	// Validate Count (1-600) - check the original value if set
	if cfg.Count != 0 && (cfg.Count < minCount || cfg.Count > maxCount) {
		return checkerdef.NewConfigErrorf("count", "must be between %d and %d, got %d", minCount, maxCount, cfg.Count)
	}

	// Validate Interval (50ms - 60s) - check the original value if set
	if cfg.Interval != 0 && (cfg.Interval < minInterval || cfg.Interval > maxInterval) {
		return checkerdef.NewConfigErrorf("interval", "must be between %s and %s, got %s", minInterval.String(), maxInterval.String(), cfg.Interval.String())
	}

	// Validate PacketSize (0 - 65507)
	if cfg.PacketSize < 0 || cfg.PacketSize > 65507 {
		return checkerdef.NewConfigErrorf("packet_size", "must be between 0 and 65507 bytes, got %d", cfg.PacketSize)
	}

	// Validate TTL (1 - 255) - check the original value if set
	if cfg.TTL != 0 && (cfg.TTL < 1 || cfg.TTL > 255) {
		return checkerdef.NewConfigErrorf("ttl", "must be between 1 and 255, got %d", cfg.TTL)
	}

	// Validate Timeout (> 0 and <= 30s) - check the original value if set
	if cfg.Timeout != 0 && (cfg.Timeout <= 0 || cfg.Timeout > 30*time.Second) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 30s, got %s", cfg.Timeout.String())
	}

	if spec.Slug == "" {
		spec.Slug = "icmp-" + strings.ReplaceAll(cfg.Host, ".", "-")
	}

	return nil
}

// Execute performs the ICMP ping check and returns the result.
//
//nolint:cyclop,funlen // Complexity necessary for comprehensive ICMP checking
func (c *ICMPChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*ICMPConfig](config)
	if err != nil {
		return nil, err
	}

	// Apply defaults
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	count := cfg.Count
	if count == 0 {
		count = defaultCount
	}

	interval := cfg.Interval
	if interval == 0 {
		interval = defaultInterval
	}

	// Resolve host to IP using context-aware resolver
	ips, err := checkerdef.LookupIPAddr(ctx, cfg.Host)
	if err != nil {
		//nolint:nilerr // Returning result with error details, not nil error
		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: 0,
			Output: map[string]any{
				checkerdef.OutputKeyHost:   cfg.Host,
				checkerdef.OutputKeyMethod: methodICMP,
				checkerdef.OutputKeyError:  "DNS resolution failed: " + err.Error(),
			},
			Metrics: map[string]any{
				metricPacketsSent:     0,
				metricPacketsReceived: 0,
				metricPacketLossPct:   float64(percentageMultiplier),
			},
		}, nil
	}

	// Pick the address to ping — IPv4-first by default (in its 4-byte form, as
	// the ICMP path has always used), or the family this check pins via
	// `ipVersion`. One shared implementation for every checker.
	ip, selectErr := checkerdef.SelectIPAddr(cfg.Host, ips, checkerdef.IPVersionFrom(ctx))
	if selectErr != nil {
		return &checkerdef.Result{
			Status:   checkerdef.IPVersionFailureStatus(selectErr),
			Duration: 0,
			Output: map[string]any{
				checkerdef.OutputKeyHost:   cfg.Host,
				checkerdef.OutputKeyMethod: methodICMP,
				checkerdef.OutputKeyError:  selectErr.Error(),
			},
			Metrics: map[string]any{
				metricPacketsSent:     0,
				metricPacketsReceived: 0,
				metricPacketLossPct:   float64(percentageMultiplier),
			},
		}, nil
	}

	isIPv6 := checkerdef.IPVersionOf(ip) == checkerdef.IPVersionIPv6

	start := time.Now()

	// Perform the ICMP burst: packets sent on schedule, replies collected
	// asynchronously (spec 2026-09-21-01).
	results, burstErr := performICMPPings(ctx, ip, isIPv6, count, timeout, interval)

	duration := time.Since(start)

	if burstErr != nil {
		// The burst could not run at all (socket setup failure, or every write
		// failed): nothing was sent, so the burst reports zero packets and a
		// clean failure rather than fabricated loss figures.
		result := &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: duration,
			Output: map[string]any{
				checkerdef.OutputKeyHost:   cfg.Host,
				checkerdef.OutputKeyMethod: methodICMP,
				"ip":                       ip.String(),
				checkerdef.OutputKeyError:  burstErr.Error(),
			},
			Metrics: map[string]any{
				metricPacketsSent:     0,
				metricPacketsReceived: 0,
				metricPacketLossPct:   float64(percentageMultiplier),
			},
		}
		result.SetNetworkFailure(checkerdef.NewNetworkFailure(
			burstFailureClass(burstErr), cfg.Host, ip.String(), 0))

		return result, nil
	}

	// Calculate statistics. `results` holds exactly the packets that were
	// actually written to the socket, so sent/received/loss describe what
	// really happened — a truncated burst shrinks packets_sent instead of
	// reporting packets it never transmitted as lost (spec 2026-09-21-01).
	sent := len(results)

	var minRTT, maxRTT, totalRTT time.Duration

	successCount := 0

	minRTT = time.Duration(1<<63 - 1) // Max duration

	var sumSquaresNS float64

	for idx := range results {
		if results[idx].Success {
			successCount++
			totalRTT += results[idx].RTT

			rttNS := float64(results[idx].RTT.Nanoseconds())
			sumSquaresNS += rttNS * rttNS

			if results[idx].RTT < minRTT {
				minRTT = results[idx].RTT
			}

			if results[idx].RTT > maxRTT {
				maxRTT = results[idx].RTT
			}
		}
	}

	// Calculate packet loss percentage over what was actually sent.
	packetLossPct := float64(percentageMultiplier)

	if sent > 0 {
		packetLossPct = float64(sent-successCount) / float64(sent) * percentageMultiplier
	}

	// Determine status
	status := checkerdef.StatusDown
	if successCount > 0 {
		status = checkerdef.StatusUp
	}

	// Determine IP version string
	ipVersion := checkerdef.IPVersionOf(ip).String()

	result := checkerdef.Result{
		Status:   status,
		Duration: duration,
		Metrics: map[string]any{
			metricPacketsSent:     sent,
			metricPacketsReceived: successCount,
			metricPacketLossPct:   packetLossPct,
		},
		Output: map[string]any{
			checkerdef.OutputKeyHost:      cfg.Host,
			checkerdef.OutputKeyMethod:    methodICMP,
			"ip":                          ip.String(),
			checkerdef.OutputKeyIPVersion: ipVersion,
		},
	}

	// Add RTT metrics if we had any successful checks
	if successCount > 0 {
		avgRTT := totalRTT / time.Duration(successCount)
		result.Metrics["rtt_ms_min"] = float64(minRTT.Microseconds()) / microsecondsToMillis
		result.Metrics["rtt_ms_max"] = float64(maxRTT.Microseconds()) / microsecondsToMillis
		result.Metrics["rtt_ms_avg"] = float64(avgRTT.Microseconds()) / microsecondsToMillis

		// Jitter: population std-dev over the successful RTTs of the burst,
		// computed in the same pass as min/max/avg (spec 2026-09-21-01). It is
		// the figure that makes this line-quality data rather than uptime
		// data. No aggregation suffix matches, so it rolls up by the
		// type-based default (float64 → average), like rtt_ms_avg.
		meanNS := float64(totalRTT) / float64(successCount)
		variance := sumSquaresNS/float64(successCount) - meanNS*meanNS

		if variance < 0 {
			variance = 0
		}

		result.Metrics[metricRTTJitterMs] = math.Sqrt(variance) / float64(time.Millisecond)
	} else {
		// Include the last error if all pings failed
		for i := len(results) - 1; i >= 0; i-- {
			if results[i].Error != nil {
				result.Output[checkerdef.OutputKeyError] = results[i].Error.Error()

				break
			}
		}

		if result.Output[checkerdef.OutputKeyError] == nil {
			result.Output[checkerdef.OutputKeyError] = "no successful ping responses"
		}

		// TOTAL loss only. Partial loss is degraded reachability, not a
		// reachability FAILURE — the target answered, and the check is up.
		result.SetNetworkFailure(checkerdef.NewNetworkFailure(
			icmpFailureClass(results), cfg.Host, ip.String(), 0))
	}

	return &result, nil
}

// icmpFailureClass distinguishes "nothing came back" from "something told us
// the destination is unreachable".
//
// It matters for reading the trace afterwards: silence usually means the trace
// will also stop somewhere short of the target, while an explicit unreachable
// means a router made a decision and the trace should show which one.
func icmpFailureClass(results []pingResult) string {
	for idx := range results {
		if results[idx].Error == nil {
			continue
		}

		switch checkerdef.ClassifyDialError(results[idx].Error, false) {
		case checkerdef.NetFailureNetworkUnreachable, checkerdef.NetFailureHostUnreachable:
			return checkerdef.NetFailureICMPUnreachable
		}
	}

	return checkerdef.NetFailureICMPTimeout
}

// burstFailureClass classifies a whole-burst failure (socket setup error, or
// every write failed) the way icmpFailureClass classifies per-packet errors.
func burstFailureClass(err error) string {
	switch checkerdef.ClassifyDialError(err, false) {
	case checkerdef.NetFailureNetworkUnreachable, checkerdef.NetFailureHostUnreachable:
		return checkerdef.NetFailureICMPUnreachable
	}

	return checkerdef.NetFailureICMPTimeout
}

// pingResult represents the result of a single ICMP ping attempt.
type pingResult struct {
	Success bool
	RTT     time.Duration
	Error   error
}
