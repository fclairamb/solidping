package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/notifications"
)

// TestInstallEgressGuard_NotSetBeforeSystemConfig pins the bug the audit
// found: building the notification-sender egress guard inside NewServer would
// freeze it at the pre-overlay config, before the DB-stored
// egress.allow_private_targets system parameter (or SP_EGRESS_ALLOW_PRIVATE)
// has had a chance to apply. After NewServer alone (no InitializeSystemConfig
// yet), the guard must not exist.
func TestInstallEgressGuard_NotSetBeforeSystemConfig(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	cfg := newEgressGuardTestConfig()

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })
	r.NoError(server.Initialize(ctx))

	r.Nil(server.Services().EgressGuard, "the guard must not be built before the system-parameter overlay runs")
}

// TestInstallEgressGuard_ReflectsDBSystemParameter is the audit's requested
// regression test: a SaaS deployment defaults to denying private targets, but
// an operator who stores egress.allow_private_targets=true as a system
// parameter (Server Settings UI, or a direct write like a support operation)
// must see that reflected in the guard InitializeSystemConfig installs — and,
// end to end, in notification sender URL validation, which is what the
// operator is actually trying to unblock.
func TestInstallEgressGuard_ReflectsDBSystemParameter(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	cfg := newEgressGuardTestConfig()

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	r.NoError(server.Initialize(ctx))

	// Without the override, a SaaS deployment denies private targets.
	loopbackURL := "http://127.0.0.1:9/hook"
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	r.NotNil(server.Services().EgressGuard)
	r.False(server.Services().EgressGuard.AllowsPrivate())
	r.Error(notifications.ValidateSenderURL(ctx, server.Services().EgressGuard, loopbackURL),
		"SaaS default must still deny a loopback sender URL")

	// An operator stores the override as a DB system parameter — exactly what
	// the denial message on every refused check/delivery tells them to do
	// (egress.DeniedError.Error: "system parameter egress.allow_private_targets").
	r.NoError(server.dbService.SetSystemParameter(ctx, egress.ParamAllowPrivate, true, false))

	// Re-running InitializeSystemConfig is what a process restart does (the
	// parameter's own doc comment: "applied at startup, take effect on the
	// next worker restart"). A fresh *config.Config mirrors a real restart
	// reading YAML/env from scratch, rather than reusing a cfg some earlier
	// overlay may have already mutated.
	cfg2 := newEgressGuardTestConfig()
	r.NoError(server.InitializeSystemConfig(ctx, cfg2))

	r.NotNil(server.Services().EgressGuard)
	r.True(server.Services().EgressGuard.AllowsPrivate(),
		"the DB-stored system parameter must flip the installed guard")
	r.NoError(notifications.ValidateSenderURL(ctx, server.Services().EgressGuard, loopbackURL),
		"sender URL validation must honor the system-parameter override, not just cfg.Deployment.Mode")
}

// newEgressGuardTestConfig is a minimal SaaS-mode config: SaaS is the
// interesting case (private targets denied by default), a fresh in-memory
// SQLite database per call, and just enough auth config for NewServer to
// build without error.
func newEgressGuardTestConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Deployment.Mode = config.DeploymentModeSaaS
	cfg.Auth.JWTSecret = "egress-guard-test-secret"
	cfg.Auth.AccessTokenExpiry = time.Hour
	cfg.Auth.RefreshTokenExpiry = 24 * time.Hour

	return cfg
}
