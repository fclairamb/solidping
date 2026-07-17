package sshtunneltest

import (
	"testing"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkssh"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel"
)

// CheckConfig is the SSH-check config that reaches this bastion with password
// auth and its real fingerprint — i.e. what a user would store on a working
// tunnel check.
func (s *Server) CheckConfig() map[string]any {
	return map[string]any{
		"host":                 s.Host,
		"port":                 float64(s.Port),
		"expected_fingerprint": s.Fingerprint,
		"username":             s.Username,
		"password":             s.Password,
	}
}

// Dialer opens a real tunnel to this bastion and returns the dialer a checker
// would receive on its context. The SSH client is closed with the test.
func (s *Server) Dialer(t *testing.T) *sshtunnel.Dialer {
	t.Helper()

	cfg := &checkssh.SSHConfig{
		Host:                s.Host,
		Port:                s.Port,
		Timeout:             10 * time.Second,
		ExpectedFingerprint: s.Fingerprint,
		Username:            s.Username,
		Password:            s.Password,
	}

	dialer, closer, err := sshtunnel.Dial(t.Context(), cfg)
	if err != nil {
		t.Fatalf("dial tunnel: %v", err)
	}

	t.Cleanup(func() { _ = closer.Close() })

	return dialer
}
