// Package checkftp provides FTP server availability checks.
package checkftp

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jlaffaye/ftp"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// FTPChecker implements the Checker interface for FTP checks.
type FTPChecker struct{}

// Type returns the check type identifier.
func (c *FTPChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeFTP
}

// Validate checks if the configuration is valid.
func (c *FTPChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &FTPConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Name == "" {
		port := cfg.Port
		if port == 0 {
			port = defaultPort
			if cfg.TLSMode == TLSModeImplicit {
				port = implicitTLSPort
			}
		}

		spec.Name = fmt.Sprintf("FTP: %s:%d", cfg.Host, port)
	}

	if spec.Slug == "" {
		spec.Slug = "ftp-" + strings.ReplaceAll(cfg.Host, ".", "-")
	}

	return nil
}

// Execute performs the FTP check.
//
//nolint:funlen // FTP protocol flow requires comprehensive logic
func (c *FTPChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*FTPConfig](config)
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
		if cfg.TLSMode == TLSModeImplicit {
			port = implicitTLSPort
		}
	}

	username := cfg.Username
	if username == "" {
		username = defaultUsername
	}

	start := time.Now()
	metrics := map[string]any{}
	output := map[string]any{
		"host": cfg.Host,
		"port": port,
	}

	target := net.JoinHostPort(cfg.Host, strconv.Itoa(port))

	// Establish connection
	connectStart := time.Now()

	var conn *ftp.ServerConn

	dialOpts := []ftp.DialOption{
		ftp.DialWithContext(ctx),
		ftp.DialWithTimeout(timeout),
	}

	switch cfg.TLSMode {
	case TLSModeImplicit:
		tlsConfig := &tls.Config{
			ServerName:         cfg.Host,
			InsecureSkipVerify: !cfg.TLSVerify,
		}

		dialOpts = append(dialOpts, ftp.DialWithTLS(tlsConfig))
	case TLSModeExplicit:
		tlsConfig := &tls.Config{
			ServerName:         cfg.Host,
			InsecureSkipVerify: !cfg.TLSVerify,
		}

		dialOpts = append(dialOpts, ftp.DialWithExplicitTLS(tlsConfig))
	}

	conn, err = ftp.Dial(target, dialOpts...)
	if err != nil {
		if ctx.Err() != nil {
			return &checkerdef.Result{
				Status:   checkerdef.StatusTimeout,
				Duration: time.Since(start),
				Output:   mergeOutput(output, map[string]any{checkerdef.OutputKeyError: "connection timeout"}),
			}, nil
		}

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Output:   mergeOutput(output, map[string]any{checkerdef.OutputKeyError: fmt.Sprintf("connection failed: %v", err)}),
		}, nil
	}

	defer func() { _ = conn.Quit() }()

	connectTime := time.Since(connectStart)
	metrics["connection_time_ms"] = float64(connectTime.Microseconds()) / microsecondsPerMs

	// Login
	authStart := time.Now()

	err = conn.Login(username, cfg.Password)
	if err != nil {
		if ctx.Err() != nil {
			return &checkerdef.Result{
				Status:   checkerdef.StatusTimeout,
				Duration: time.Since(start),
				Metrics:  metrics,
				Output:   mergeOutput(output, map[string]any{checkerdef.OutputKeyError: "authentication timeout"}),
			}, nil
		}

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   mergeOutput(output, map[string]any{checkerdef.OutputKeyError: fmt.Sprintf("login failed: %v", err)}),
		}, nil
	}

	authTime := time.Since(authStart)
	metrics["auth_time_ms"] = float64(authTime.Microseconds()) / microsecondsPerMs

	// Verify path if specified
	if cfg.Path != "" {
		_, err = conn.List(cfg.Path)
		if err != nil {
			return &checkerdef.Result{
				Status:   checkerdef.StatusDown,
				Duration: time.Since(start),
				Metrics:  metrics,
				Output: mergeOutput(output, map[string]any{
					checkerdef.OutputKeyError: fmt.Sprintf("path verification failed: %v", err),
				}),
			}, nil
		}
	}

	metrics["total_time_ms"] = float64(time.Since(start).Microseconds()) / microsecondsPerMs

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}, nil
}

func mergeOutput(base, extra map[string]any) map[string]any {
	result := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		result[k] = v
	}

	for k, v := range extra {
		result[k] = v
	}

	return result
}
