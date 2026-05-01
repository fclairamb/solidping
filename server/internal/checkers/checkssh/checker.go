// Package checkssh provides SSH server availability checks.
package checkssh

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

var errFingerprintMismatch = errors.New("fingerprint mismatch")

const (
	defaultPort    = 22
	defaultTimeout = 10 * time.Second
	maxOutputSize  = 4 * 1024
	msPerMicro     = 1000.0
)

// SSHChecker implements the Checker interface for SSH checks.
type SSHChecker struct{}

// Type returns the check type identifier.
func (c *SSHChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeSSH
}

// Validate checks if the configuration is valid.
func (c *SSHChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &SSHConfig{}
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
		}

		spec.Name = fmt.Sprintf("%s:%d", cfg.Host, port)
	}

	if spec.Slug == "" {
		spec.Slug = strings.ReplaceAll(cfg.Host, ".", "-")
	}

	return nil
}

// Execute performs the SSH check.
func (c *SSHChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*SSHConfig](config)
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

	start := time.Now()

	// Resolve host
	resolver := &net.Resolver{}

	addrs, err := resolver.LookupIPAddr(ctx, cfg.Host)
	if err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output:   map[string]any{checkerdef.OutputKeyError: fmt.Sprintf("DNS resolution failed: %v", err)},
		}, nil
	}

	if len(addrs) == 0 {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output:   map[string]any{checkerdef.OutputKeyError: "no IP addresses found"},
		}, nil
	}

	// Prefer IPv4
	var targetIP net.IP

	for i := range addrs {
		if addrs[i].IP.To4() != nil {
			targetIP = addrs[i].IP

			break
		}
	}

	if targetIP == nil {
		targetIP = addrs[0].IP
	}

	target := net.JoinHostPort(targetIP.String(), strconv.Itoa(port))
	output := map[string]any{"host": targetIP.String(), "port": port}

	if cfg.Username == "" {
		result := c.executeBannerOnly(ctx, target, cfg, timeout, output)
		result.Duration = time.Since(start)

		return &result, nil
	}

	result := c.executeWithAuth(ctx, target, cfg, timeout, output)
	result.Duration = time.Since(start)

	return &result, nil
}

// executeBannerOnly connects via TCP and reads the SSH banner.
func (c *SSHChecker) executeBannerOnly(
	ctx context.Context, target string, cfg *SSHConfig, timeout time.Duration, output map[string]any,
) checkerdef.Result {
	ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	connectStart := time.Now()
	dialer := &net.Dialer{}

	conn, err := dialer.DialContext(ctxTimeout, "tcp", target)
	if err != nil {
		if ctxTimeout.Err() != nil {
			return checkerdef.Result{
				Status: checkerdef.StatusTimeout,
				Output: map[string]any{checkerdef.OutputKeyError: "connection timeout"},
			}
		}

		return checkerdef.Result{
			Status: checkerdef.StatusDown,
			Output: map[string]any{checkerdef.OutputKeyError: fmt.Sprintf("connection failed: %v", err)},
		}
	}

	defer func() { _ = conn.Close() }()

	connectTime := time.Since(connectStart)
	metrics := map[string]any{"connection_time_ms": float64(connectTime.Microseconds()) / msPerMicro}

	// Read banner
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return checkerdef.Result{Status: checkerdef.StatusError, Metrics: metrics, Output: output}
	}

	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return checkerdef.Result{
			Status:  checkerdef.StatusDown,
			Metrics: metrics,
			Output:  mergeOutput(output, map[string]any{checkerdef.OutputKeyError: "no banner received"}),
		}
	}

	banner := scanner.Text()
	output["banner"] = banner

	if !strings.HasPrefix(banner, "SSH-") {
		return checkerdef.Result{
			Status:  checkerdef.StatusDown,
			Metrics: metrics,
			Output:  mergeOutput(output, map[string]any{checkerdef.OutputKeyError: "invalid SSH banner"}),
		}
	}

	// If fingerprint verification requested, do a full SSH handshake
	if cfg.ExpectedFingerprint != "" {
		return c.verifyFingerprint(ctx, target, cfg, timeout, metrics, output)
	}

	return checkerdef.Result{Status: checkerdef.StatusUp, Metrics: metrics, Output: output}
}

// verifyFingerprint performs an SSH handshake to capture and compare the host key fingerprint.
func (c *SSHChecker) verifyFingerprint(
	ctx context.Context, target string, cfg *SSHConfig, timeout time.Duration,
	metrics map[string]any, output map[string]any,
) checkerdef.Result {
	var capturedFingerprint string

	clientConfig := &ssh.ClientConfig{
		User: "probe",
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			hash := sha256.Sum256(key.Marshal())
			capturedFingerprint = "SHA256:" + base64.StdEncoding.EncodeToString(hash[:])

			return nil
		},
		Auth:    []ssh.AuthMethod{ssh.Password("")},
		Timeout: timeout,
	}

	// We expect auth to fail, but we'll capture the fingerprint during handshake
	conn, err := ssh.Dial("tcp", target, clientConfig)
	if conn != nil {
		_ = conn.Close()
	}

	// Auth failure is expected — we only care about the fingerprint
	if capturedFingerprint == "" && err != nil {
		return checkerdef.Result{
			Status:  checkerdef.StatusDown,
			Metrics: metrics,
			Output: mergeOutput(output, map[string]any{
				checkerdef.OutputKeyError: fmt.Sprintf("SSH handshake failed: %v", err),
			}),
		}
	}

	output["fingerprint"] = capturedFingerprint
	_ = ctx

	if capturedFingerprint != cfg.ExpectedFingerprint {
		return checkerdef.Result{
			Status:  checkerdef.StatusDown,
			Metrics: metrics,
			Output: mergeOutput(output, map[string]any{
				checkerdef.OutputKeyError: fmt.Sprintf("fingerprint mismatch: got %s, expected %s",
					capturedFingerprint, cfg.ExpectedFingerprint),
			}),
		}
	}

	return checkerdef.Result{Status: checkerdef.StatusUp, Metrics: metrics, Output: output}
}

// executeWithAuth performs SSH authentication and optionally runs a command.
//
//nolint:funlen,cyclop // SSH auth + command execution requires comprehensive logic
func (c *SSHChecker) executeWithAuth(
	ctx context.Context, target string, cfg *SSHConfig, timeout time.Duration, output map[string]any,
) checkerdef.Result {
	metrics := map[string]any{}

	// Build auth methods
	var authMethods []ssh.AuthMethod

	if cfg.Password != "" {
		authMethods = append(authMethods, ssh.Password(cfg.Password))
	} else if cfg.PrivateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(cfg.PrivateKey))
		if err != nil {
			return checkerdef.Result{
				Status: checkerdef.StatusError,
				Output: mergeOutput(output, map[string]any{checkerdef.OutputKeyError: fmt.Sprintf("invalid private key: %v", err)}),
			}
		}

		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}

	// Build host key callback
	hostKeyCallback := ssh.InsecureIgnoreHostKey()

	if cfg.ExpectedFingerprint != "" {
		hostKeyCallback = func(_ string, _ net.Addr, key ssh.PublicKey) error {
			hash := sha256.Sum256(key.Marshal())
			fingerprint := "SHA256:" + base64.StdEncoding.EncodeToString(hash[:])
			output["fingerprint"] = fingerprint

			if fingerprint != cfg.ExpectedFingerprint {
				return fmt.Errorf("%w: got %s, expected %s",
					errFingerprintMismatch, fingerprint, cfg.ExpectedFingerprint)
			}

			return nil
		}
	}

	clientConfig := &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         timeout,
		BannerCallback: func(message string) error {
			output["banner"] = strings.TrimSpace(message)

			return nil
		},
	}

	// Connect
	authStart := time.Now()

	conn, err := ssh.Dial("tcp", target, clientConfig)
	if err != nil {
		if ctx.Err() != nil {
			return checkerdef.Result{
				Status: checkerdef.StatusTimeout, Metrics: metrics,
				Output: mergeOutput(output, map[string]any{checkerdef.OutputKeyError: "SSH connection timeout"}),
			}
		}

		return checkerdef.Result{
			Status: checkerdef.StatusDown, Metrics: metrics,
			Output: mergeOutput(output, map[string]any{
				checkerdef.OutputKeyError: fmt.Sprintf("SSH connection failed: %v", err),
			}),
		}
	}

	defer func() { _ = conn.Close() }()

	authTime := time.Since(authStart)
	metrics["auth_time_ms"] = float64(authTime.Microseconds()) / msPerMicro

	// If no command, just verify connectivity
	if cfg.Command == "" {
		return checkerdef.Result{Status: checkerdef.StatusUp, Metrics: metrics, Output: output}
	}

	// Execute command
	session, err := conn.NewSession()
	if err != nil {
		return checkerdef.Result{
			Status: checkerdef.StatusDown, Metrics: metrics,
			Output: mergeOutput(output, map[string]any{
				checkerdef.OutputKeyError: fmt.Sprintf("failed to create session: %v", err),
			}),
		}
	}

	defer func() { _ = session.Close() }()

	cmdStart := time.Now()

	rawOutput, err := session.CombinedOutput(cfg.Command)
	cmdTime := time.Since(cmdStart)
	metrics["command_time_ms"] = float64(cmdTime.Microseconds()) / msPerMicro

	// Capture output (truncate to maxOutputSize)
	stdoutStr := string(rawOutput)
	if len(stdoutStr) > maxOutputSize {
		stdoutStr = stdoutStr[:maxOutputSize]
	}

	output["stdout"] = stdoutStr

	// Check exit code
	exitCode := 0

	if err != nil {
		var exitErr *ssh.ExitError
		if ok := isExitError(err, &exitErr); ok {
			exitCode = exitErr.ExitStatus()
		} else {
			return checkerdef.Result{
				Status: checkerdef.StatusDown, Metrics: metrics,
				Output: mergeOutput(output, map[string]any{
					checkerdef.OutputKeyError: fmt.Sprintf("command execution failed: %v", err),
				}),
			}
		}
	}

	output["exit_code"] = exitCode

	if exitCode != cfg.ExpectedExitCode {
		return checkerdef.Result{
			Status: checkerdef.StatusDown, Metrics: metrics,
			Output: mergeOutput(output, map[string]any{
				checkerdef.OutputKeyError: fmt.Sprintf("exit code %d, expected %d", exitCode, cfg.ExpectedExitCode),
			}),
		}
	}

	// Check expected output
	if cfg.ExpectedOutput != "" && !strings.Contains(stdoutStr, cfg.ExpectedOutput) {
		return checkerdef.Result{
			Status: checkerdef.StatusDown, Metrics: metrics,
			Output: mergeOutput(output, map[string]any{
				checkerdef.OutputKeyError: fmt.Sprintf("expected output %q not found", cfg.ExpectedOutput),
			}),
		}
	}

	// Check expected output pattern
	if cfg.ExpectedOutputPattern != "" {
		outputRegex := cfg.outputPatternRegex
		if outputRegex == nil {
			var compileErr error
			outputRegex, compileErr = regexp.Compile(cfg.ExpectedOutputPattern)

			if compileErr != nil {
				return checkerdef.Result{
					Status: checkerdef.StatusError, Metrics: metrics,
					Output: mergeOutput(output, map[string]any{
						checkerdef.OutputKeyError: fmt.Sprintf("invalid regex: %v", compileErr),
					}),
				}
			}
		}

		if !outputRegex.MatchString(stdoutStr) {
			return checkerdef.Result{
				Status: checkerdef.StatusDown, Metrics: metrics,
				Output: mergeOutput(output, map[string]any{
					checkerdef.OutputKeyError: fmt.Sprintf("output does not match pattern %q", cfg.ExpectedOutputPattern),
				}),
			}
		}
	}

	return checkerdef.Result{Status: checkerdef.StatusUp, Metrics: metrics, Output: output}
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

func isExitError(err error, target **ssh.ExitError) bool {
	return errors.As(err, target)
}
