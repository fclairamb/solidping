// Package checkicmp provides ICMP ping monitoring checks.
package checkicmp

import (
	"context"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	// Default values from spec.
	defaultTimeout    = 5 * time.Second
	defaultCount      = 1
	defaultInterval   = 1 * time.Second
	defaultPacketSize = 56

	// Network constants.
	percentageMultiplier = 100    // Multiplier for percentage calculations
	microsecondsToMillis = 1000.0 // Conversion factor from microseconds to milliseconds

	// ICMP protocol numbers.
	protocolICMP   = 1
	protocolICMPv6 = 58

	methodICMP = "icmp"
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

	// Validate Count (1-10) - check the original value if set
	if cfg.Count != 0 && (cfg.Count < 1 || cfg.Count > 10) {
		return checkerdef.NewConfigErrorf("count", "must be between 1 and 10, got %d", cfg.Count)
	}

	// Validate Interval (100ms - 60s) - check the original value if set
	if cfg.Interval != 0 && (cfg.Interval < 100*time.Millisecond || cfg.Interval > 60*time.Second) {
		return checkerdef.NewConfigErrorf("interval", "must be between 100ms and 60s, got %s", cfg.Interval.String())
	}

	// Validate PacketSize (0 - 65507)
	if cfg.PacketSize < 0 || cfg.PacketSize > 65507 {
		return checkerdef.NewConfigErrorf("packet_size", "must be between 0 and 65507 bytes, got %d", cfg.PacketSize)
	}

	// Validate TTL (1 - 255) - check the original value if set
	if cfg.TTL != 0 && (cfg.TTL < 1 || cfg.TTL > 255) {
		return checkerdef.NewConfigErrorf("ttl", "must be between 1 and 255, got %d", cfg.TTL)
	}

	// Validate Timeout (> 0 and <= 60s) - check the original value if set
	if cfg.Timeout != 0 && (cfg.Timeout <= 0 || cfg.Timeout > 60*time.Second) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 60s, got %s", cfg.Timeout.String())
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
	resolver := net.DefaultResolver
	ips, err := resolver.LookupIPAddr(ctx, cfg.Host)
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
				"packets_sent":     0,
				"packets_received": 0,
				"packet_loss_pct":  float64(percentageMultiplier),
			},
		}, nil
	}

	// Pick first IPv4, or first IPv6 if no IPv4 available
	var ip net.IP

	var isIPv6 bool

	for idx := range ips {
		if v4 := ips[idx].IP.To4(); v4 != nil {
			ip = v4
			isIPv6 = false

			break
		}

		if ip == nil {
			ip = ips[idx].IP
			isIPv6 = true
		}
	}

	start := time.Now()

	// Perform ICMP pings
	results := performICMPPings(ctx, ip, isIPv6, count, timeout, interval)

	duration := time.Since(start)

	// Calculate statistics
	var minRTT, maxRTT, totalRTT time.Duration

	successCount := 0

	minRTT = time.Duration(1<<63 - 1) // Max duration

	for idx := range results {
		if results[idx].Success {
			successCount++
			totalRTT += results[idx].RTT

			if results[idx].RTT < minRTT {
				minRTT = results[idx].RTT
			}

			if results[idx].RTT > maxRTT {
				maxRTT = results[idx].RTT
			}
		}
	}

	// Calculate packet loss percentage
	packetLossPct := float64(count-successCount) / float64(count) * percentageMultiplier

	// Determine status
	status := checkerdef.StatusDown
	if successCount > 0 {
		status = checkerdef.StatusUp
	}

	// Determine IP version string
	ipVersion := "ipv4"
	if isIPv6 {
		ipVersion = "ipv6"
	}

	result := checkerdef.Result{
		Status:   status,
		Duration: duration,
		Metrics: map[string]any{
			"packets_sent":     count,
			"packets_received": successCount,
			"packet_loss_pct":  packetLossPct,
		},
		Output: map[string]any{
			checkerdef.OutputKeyHost:   cfg.Host,
			checkerdef.OutputKeyMethod: methodICMP,
			"ip":                       ip.String(),
			"ip_version":               ipVersion,
		},
	}

	// Add RTT metrics if we had any successful checks
	if successCount > 0 {
		avgRTT := totalRTT / time.Duration(successCount)
		result.Metrics["rtt_ms_min"] = float64(minRTT.Microseconds()) / microsecondsToMillis
		result.Metrics["rtt_ms_max"] = float64(maxRTT.Microseconds()) / microsecondsToMillis
		result.Metrics["rtt_ms_avg"] = float64(avgRTT.Microseconds()) / microsecondsToMillis
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
	}

	return &result, nil
}

// pingResult represents the result of a single ICMP ping attempt.
type pingResult struct {
	Success bool
	RTT     time.Duration
	Error   error
}

// performICMPPings performs multiple ICMP ping attempts.
func performICMPPings(
	ctx context.Context,
	ip net.IP,
	isIPv6 bool,
	count int,
	timeout, interval time.Duration,
) []pingResult {
	results := make([]pingResult, 0, count)

	for i := 0; i < count; i++ {
		// Check if context is canceled
		if ctx.Err() != nil {
			results = append(results, pingResult{
				Success: false,
				Error:   ctx.Err(),
			})

			continue
		}

		// Perform single ICMP ping
		result := performSingleICMPPing(ctx, ip, isIPv6, timeout, i)
		results = append(results, result)

		// Sleep between attempts (except for the last one)
		if i < count-1 {
			select {
			case <-ctx.Done():
				return results
			case <-time.After(interval):
			}
		}
	}

	return results
}

// performSingleICMPPing performs a single ICMP Echo Request/Reply exchange.
//
//nolint:funlen,cyclop,gocognit // ICMP implementation requires careful handling of multiple cases
func performSingleICMPPing(ctx context.Context, ip net.IP, isIPv6 bool, timeout time.Duration, seq int) pingResult {
	// Determine network type and protocol
	var network, listenAddr string

	var proto int

	var msgType icmp.Type

	if isIPv6 {
		proto = protocolICMPv6
		msgType = ipv6.ICMPTypeEchoRequest
		listenAddr = "::"
	} else {
		proto = protocolICMP
		msgType = ipv4.ICMPTypeEcho
		listenAddr = "0.0.0.0"
	}

	// Try unprivileged mode first (udp4/udp6), then fall back to privileged (ip4:icmp/ip6:ipv6-icmp)
	var conn *icmp.PacketConn

	var connErr error

	var useUDP bool

	if isIPv6 {
		network = "udp6"
	} else {
		network = "udp4"
	}

	conn, connErr = icmp.ListenPacket(network, listenAddr)
	if connErr == nil {
		useUDP = true
	} else {
		// Fall back to privileged mode
		if isIPv6 {
			network = "ip6:ipv6-icmp"
		} else {
			network = "ip4:icmp"
		}

		conn, connErr = icmp.ListenPacket(network, listenAddr)
		if connErr != nil {
			return pingResult{Success: false, Error: connErr}
		}

		useUDP = false
	}

	defer func() { _ = conn.Close() }()

	// Create context with timeout
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Set deadline on connection
	deadline, _ := pingCtx.Deadline()
	if deadlineErr := conn.SetDeadline(deadline); deadlineErr != nil {
		return pingResult{Success: false, Error: deadlineErr}
	}

	// Build ICMP Echo Request message
	// Use process ID as identifier (masked to 16 bits)
	id := os.Getpid() & 0xffff

	msg := &icmp.Message{
		Type: msgType,
		Code: 0,
		Body: &icmp.Echo{
			ID:   id,
			Seq:  seq,
			Data: make([]byte, defaultPacketSize),
		},
	}

	msgBytes, marshalErr := msg.Marshal(nil)
	if marshalErr != nil {
		return pingResult{Success: false, Error: marshalErr}
	}

	// Determine destination address based on network type
	var dst net.Addr

	if useUDP {
		dst = &net.UDPAddr{IP: ip}
	} else {
		dst = &net.IPAddr{IP: ip}
	}

	// Send ICMP Echo Request
	start := time.Now()

	if _, writeErr := conn.WriteTo(msgBytes, dst); writeErr != nil {
		return pingResult{Success: false, Error: writeErr}
	}

	// Wait for ICMP Echo Reply
	reply := make([]byte, 1500)

	for {
		// Check context cancellation
		select {
		case <-pingCtx.Done():
			return pingResult{Success: false, Error: pingCtx.Err()}
		default:
		}

		bytesRead, _, readErr := conn.ReadFrom(reply)
		if readErr != nil {
			return pingResult{Success: false, Error: readErr}
		}

		rtt := time.Since(start)

		// Parse ICMP message
		replyMsg, parseErr := icmp.ParseMessage(proto, reply[:bytesRead])
		if parseErr != nil {
			continue // Invalid message, keep waiting
		}

		// Check if it's an Echo Reply
		var expectedReplyType icmp.Type
		if isIPv6 {
			expectedReplyType = ipv6.ICMPTypeEchoReply
		} else {
			expectedReplyType = ipv4.ICMPTypeEchoReply
		}

		if replyMsg.Type != expectedReplyType {
			continue // Not an echo reply, keep waiting
		}

		// Verify it's our reply by checking ID and sequence number
		echo, ok := replyMsg.Body.(*icmp.Echo)
		if !ok {
			continue
		}

		// In UDP mode on some systems, the kernel may modify the ID
		// So we primarily check the sequence number
		if useUDP || echo.ID == id {
			if echo.Seq == seq {
				return pingResult{
					Success: true,
					RTT:     rtt,
				}
			}
		}

		// Check if this is a response to our sequence but with different ID
		// (some systems rewrite the ID in UDP mode)
		if strings.HasPrefix(network, "udp") && echo.Seq == seq {
			return pingResult{
				Success: true,
				RTT:     rtt,
			}
		}
	}
}
