// Package checksftp provides SFTP server availability checks.
package checksftp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checksftp/config"
	"github.com/fclairamb/solidping/server/internal/checkers/checkssh"
	"github.com/fclairamb/solidping/server/internal/sshauth"
)

// outputKeyHostKeyFingerprint names the result-output key carrying the host
// key the server actually presented. It is written on every run, pinned or
// not, and matches the config key an operator pastes it into.
const outputKeyHostKeyFingerprint = "host_key_fingerprint"

// errHostKeyMismatch is returned by the host key callback so the dial error
// can be told apart from a network failure.
var errHostKeyMismatch = errors.New("host key mismatch")

// SFTPChecker implements the Checker interface for SFTP checks.
type SFTPChecker struct{}

// Type returns the check type identifier.
func (c *SFTPChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeSFTP
}

// Validate checks if the configuration is valid. Every rule lives in the light
// `config` sub-package so an offline validator (`sp checks validate`) can run it
// without linking this checker's execution client.
func (c *SFTPChecker) Validate(spec *checkerdef.CheckSpec) error {
	return checkconfig.ValidateSpec(spec)
}

// Execute performs the SFTP check.
//
//nolint:funlen // SFTP protocol flow requires comprehensive logic
func (c *SFTPChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*SFTPConfig](config)
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
	metrics := map[string]any{}
	output := map[string]any{
		checkerdef.OutputKeyHost: cfg.Host,
		checkerdef.OutputKeyPort: port,
	}

	target := net.JoinHostPort(cfg.Host, strconv.Itoa(port))

	// Build SSH auth methods
	var authMethods []ssh.AuthMethod

	if cfg.Password != "" {
		authMethods = append(authMethods, sshauth.PasswordMethods(cfg.Password)...)
	} else if cfg.PrivateKey != "" {
		signer, parseErr := ssh.ParsePrivateKey([]byte(cfg.PrivateKey))
		if parseErr != nil {
			return &checkerdef.Result{
				Status:   checkerdef.StatusError,
				Duration: time.Since(start),
				Output: mergeOutput(output, map[string]any{
					checkerdef.OutputKeyError: fmt.Sprintf("invalid private key: %v", parseErr),
				}),
			}, nil
		}

		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}

	var observedFingerprint string

	sshConfig := &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback(cfg.HostKeyFingerprint, output, &observedFingerprint),
		Timeout:         timeout,
	}

	// Establish SSH connection
	connectStart := time.Now()

	dialer := &net.Dialer{Timeout: timeout}

	netConn, err := dialer.DialContext(ctx, "tcp", target)
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
			Output: mergeOutput(output, map[string]any{
				checkerdef.OutputKeyError: fmt.Sprintf("connection failed: %v", err),
			}),
		}, nil
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(netConn, target, sshConfig)
	if err != nil {
		_ = netConn.Close()

		if errors.Is(err, errHostKeyMismatch) {
			return &checkerdef.Result{
				Status:   checkerdef.StatusDown,
				Duration: time.Since(start),
				Output: mergeOutput(output, map[string]any{
					checkerdef.OutputKeyError: fmt.Sprintf(
						"host key mismatch: got %s, expected %s",
						observedFingerprint, cfg.HostKeyFingerprint),
				}),
			}, nil
		}

		if ctx.Err() != nil {
			return &checkerdef.Result{
				Status:   checkerdef.StatusTimeout,
				Duration: time.Since(start),
				Output:   mergeOutput(output, map[string]any{checkerdef.OutputKeyError: "SSH handshake timeout"}),
			}, nil
		}

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Output: mergeOutput(output, map[string]any{
				checkerdef.OutputKeyError: fmt.Sprintf("SSH connection failed: %v", err),
			}),
		}, nil
	}

	sshClient := ssh.NewClient(sshConn, chans, reqs)
	defer func() { _ = sshClient.Close() }()

	connectTime := time.Since(connectStart)
	metrics["connection_time_ms"] = float64(connectTime.Microseconds()) / microsecondsPerMs

	// Create SFTP client
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output: mergeOutput(output, map[string]any{
				checkerdef.OutputKeyError: fmt.Sprintf("SFTP session failed: %v", err),
			}),
		}, nil
	}

	defer func() { _ = sftpClient.Close() }()

	// Verify path if specified
	if cfg.Path != "" {
		_, err = sftpClient.Stat(cfg.Path)
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

// hostKeyCallback builds the SSH host key callback for an SFTP check.
//
// The observed key is ALWAYS recorded in the output, pinned or not, so an
// operator can read it off a passing check and paste it into
// host_key_fingerprint. With `pin` set the comparison happens INSIDE the
// callback (mirroring checkssh.verifyFingerprint) so the handshake itself
// refuses an unexpected key; with no pin the callback accepts anything, which
// is what a reachability probe against a host the operator owns needs.
func hostKeyCallback(pin string, output map[string]any, observed *string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		*observed = checkssh.Fingerprint(key)
		output[outputKeyHostKeyFingerprint] = *observed

		if pin != "" && *observed != pin {
			return fmt.Errorf("%w: got %s, expected %s", errHostKeyMismatch, *observed, pin)
		}

		return nil
	}
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
