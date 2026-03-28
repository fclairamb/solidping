package checkudp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	defaultTimeout       = 5 * time.Second
	maxReadSize          = 4 * 1024
	maxOutputDataSize    = 1024
	microsecondsPerMilli = 1000.0
)

// UDPChecker implements the Checker interface for UDP port checks.
type UDPChecker struct{}

// Type returns the check type identifier.
func (c *UDPChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeUDP
}

// Validate checks if the configuration is valid.
func (c *UDPChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &UDPConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if cfg.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	if cfg.Port == 0 {
		return checkerdef.NewConfigError("port", "is required")
	}

	if cfg.Port < 1 || cfg.Port > 65535 {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", cfg.Port)
	}

	if cfg.Timeout != 0 && (cfg.Timeout <= 0 || cfg.Timeout > 60*time.Second) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 60s, got %s", cfg.Timeout.String())
	}

	// Auto-generate name and slug from host if not provided
	if spec.Name == "" {
		spec.Name = fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	}

	if spec.Slug == "" {
		spec.Slug = strings.ReplaceAll(cfg.Host, ".", "-")
	}

	return nil
}

// Execute performs the UDP check and returns the result.
func (c *UDPChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*UDPConfig](config)
	if err != nil {
		return nil, err
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	start := time.Now()

	// Resolve hostname
	resolver := &net.Resolver{}

	addrs, err := resolver.LookupIPAddr(ctx, cfg.Host)
	if err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output: map[string]any{
				"error": fmt.Sprintf("failed to resolve hostname: %v", err),
			},
		}, nil
	}

	if len(addrs) == 0 {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output: map[string]any{
				"error": "no IP addresses found for host",
			},
		}, nil
	}

	// Prefer IPv4
	var targetIP net.IP

	var isIPv6 bool

	//nolint:gocritic // rangeValCopy: IPAddr is small enough
	for _, addr := range addrs {
		if addr.IP.To4() != nil {
			targetIP = addr.IP

			break
		}
	}

	if targetIP == nil {
		targetIP = addrs[0].IP
		isIPv6 = targetIP.To4() == nil
	}

	result := c.connect(ctx, targetIP, cfg, timeout)
	result.Duration = time.Since(start)

	ipVersion := "ipv4"
	if isIPv6 {
		ipVersion = "ipv6"
	}

	if result.Output == nil {
		result.Output = make(map[string]any)
	}

	result.Output["host"] = targetIP.String()
	result.Output["port"] = cfg.Port
	result.Output["ip_version"] = ipVersion

	return &result, nil
}

// connect performs the actual UDP operation.
//
//nolint:funlen,nestif // UDP connection logic requires comprehensive handling
func (c *UDPChecker) connect(
	ctx context.Context,
	targetIP net.IP,
	cfg *UDPConfig,
	timeout time.Duration,
) checkerdef.Result {
	ctxWithTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	target := net.JoinHostPort(targetIP.String(), strconv.Itoa(cfg.Port))

	dialer := &net.Dialer{}

	conn, err := dialer.DialContext(ctxWithTimeout, "udp", target)
	if err != nil {
		if ctxWithTimeout.Err() != nil {
			return checkerdef.Result{
				Status: checkerdef.StatusTimeout,
				Output: map[string]any{"error": "connection timeout"},
			}
		}

		return checkerdef.Result{
			Status: checkerdef.StatusDown,
			Output: map[string]any{"error": fmt.Sprintf("dial failed: %v", err)},
		}
	}

	defer func() { _ = conn.Close() }()

	metrics := map[string]any{}
	output := map[string]any{}

	// Send data if specified
	if cfg.SendData != "" {
		if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
			return checkerdef.Result{
				Status:  checkerdef.StatusError,
				Metrics: metrics,
				Output:  map[string]any{"error": fmt.Sprintf("failed to set write deadline: %v", err)},
			}
		}

		bytesSent, writeErr := conn.Write([]byte(cfg.SendData))
		if writeErr != nil {
			return checkerdef.Result{
				Status:  checkerdef.StatusDown,
				Metrics: metrics,
				Output:  map[string]any{"error": fmt.Sprintf("failed to send data: %v", writeErr)},
			}
		}

		metrics["bytes_sent"] = bytesSent
	}

	// Read response if send_data or expect_data is set
	if cfg.SendData != "" || cfg.ExpectData != "" {
		if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return checkerdef.Result{
				Status:  checkerdef.StatusError,
				Metrics: metrics,
				Output:  map[string]any{"error": fmt.Sprintf("failed to set read deadline: %v", err)},
			}
		}

		buf := make([]byte, maxReadSize)

		bytesRead, readErr := conn.Read(buf)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			if cfg.ExpectData != "" {
				return checkerdef.Result{
					Status:  checkerdef.StatusDown,
					Metrics: metrics,
					Output:  map[string]any{"error": fmt.Sprintf("failed to read response: %v", readErr)},
				}
			}
		} else {
			metrics["bytes_received"] = bytesRead

			receivedData := string(buf[:bytesRead])
			if bytesRead > maxOutputDataSize {
				receivedData = string(buf[:maxOutputDataSize])
			}

			output["received_data"] = receivedData

			// Validate expected data
			if cfg.ExpectData != "" && !strings.Contains(receivedData, cfg.ExpectData) {
				return checkerdef.Result{
					Status:  checkerdef.StatusDown,
					Metrics: metrics,
					Output: map[string]any{
						"error":         fmt.Sprintf("expected data not found: '%s'", cfg.ExpectData),
						"received_data": receivedData,
					},
				}
			}
		}
	}

	return checkerdef.Result{
		Status:  checkerdef.StatusUp,
		Metrics: metrics,
		Output:  output,
	}
}
