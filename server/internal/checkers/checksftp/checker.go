package checksftp

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// SFTPChecker implements the Checker interface for SFTP checks.
type SFTPChecker struct{}

// Type returns the check type identifier.
func (c *SFTPChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeSFTP
}

// Validate checks if the configuration is valid.
func (c *SFTPChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &SFTPConfig{}
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

		spec.Name = fmt.Sprintf("SFTP: %s:%d", cfg.Host, port)
	}

	if spec.Slug == "" {
		spec.Slug = "sftp-" + strings.ReplaceAll(cfg.Host, ".", "-")
	}

	return nil
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
		"host": cfg.Host,
		"port": port,
	}

	target := net.JoinHostPort(cfg.Host, strconv.Itoa(port))

	// Build SSH auth methods
	var authMethods []ssh.AuthMethod

	if cfg.Password != "" {
		authMethods = append(authMethods, ssh.Password(cfg.Password))
	} else if cfg.PrivateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(cfg.PrivateKey))
		if err != nil {
			return &checkerdef.Result{
				Status:   checkerdef.StatusError,
				Duration: time.Since(start),
				Output:   mergeOutput(output, map[string]any{"error": fmt.Sprintf("invalid private key: %v", err)}),
			}, nil
		}

		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}

	sshConfig := &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
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
				Output:   mergeOutput(output, map[string]any{"error": "connection timeout"}),
			}, nil
		}

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Output:   mergeOutput(output, map[string]any{"error": fmt.Sprintf("connection failed: %v", err)}),
		}, nil
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(netConn, target, sshConfig)
	if err != nil {
		_ = netConn.Close()

		if ctx.Err() != nil {
			return &checkerdef.Result{
				Status:   checkerdef.StatusTimeout,
				Duration: time.Since(start),
				Output:   mergeOutput(output, map[string]any{"error": "SSH handshake timeout"}),
			}, nil
		}

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Output:   mergeOutput(output, map[string]any{"error": fmt.Sprintf("SSH connection failed: %v", err)}),
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
			Output:   mergeOutput(output, map[string]any{"error": fmt.Sprintf("SFTP session failed: %v", err)}),
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
					"error": fmt.Sprintf("path verification failed: %v", err),
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
