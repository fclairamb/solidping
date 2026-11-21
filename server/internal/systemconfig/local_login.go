package systemconfig

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ErrNoSSOProvider is returned when local login is being switched off while
// no SSO provider is configured and enabled: the instance would lock everyone
// out (spec 2026-11-21-02).
var ErrNoSSOProvider = errors.New("local login cannot be disabled while no SSO provider is configured and enabled")

// SystemParameterReader is the slice of db.Service ResolveLocalLogin needs.
type SystemParameterReader interface {
	GetSystemParameter(ctx context.Context, key string) (*models.Parameter, error)
}

// LocalLoginState is the resolved auth.local_login.enabled.
type LocalLoginState struct {
	// Configured is the setting as the operator wrote it: env, else the DB
	// parameter, else the startup config (default true).
	Configured bool
	// Enabled is what the server enforces. It differs from Configured only
	// when local login is configured off while no SSO provider is available:
	// the server then fails open and keeps local login on.
	Enabled bool
}

// FailedOpen reports whether local login is configured off but kept on
// because no SSO provider is available.
func (s LocalLoginState) FailedOpen() bool {
	return !s.Configured && s.Enabled
}

// ResolveLocalLogin resolves auth.local_login.enabled with the env > DB >
// startup config precedence, read fresh on every call so a super admin's DB
// toggle takes effect without a restart. A DB read error falls back to the
// startup config. store may be nil (env and config only).
func ResolveLocalLogin(ctx context.Context, store SystemParameterReader, cfg *config.Config) LocalLoginState {
	configured := cfg.Auth.LocalLoginEnabled

	if envVal := os.Getenv(config.EnvLocalLoginEnabled); envVal != "" {
		configured = parseBool(envVal, configured)
	} else if val, ok := storedLocalLogin(ctx, store); ok {
		configured = parseBool(val, configured)
	}

	state := LocalLoginState{Configured: configured, Enabled: configured}
	if !configured && len(cfg.SSOProviders()) == 0 {
		state.Enabled = true
	}

	return state
}

// storedLocalLogin reads the DB value of auth.local_login.enabled.
func storedLocalLogin(ctx context.Context, store SystemParameterReader) (any, bool) {
	if store == nil {
		return nil, false
	}

	param, err := store.GetSystemParameter(ctx, string(KeyLocalLoginEnabled))
	if err != nil {
		slog.WarnContext(ctx, "Failed to read auth.local_login.enabled, using the startup value", "error", err)

		return nil, false
	}

	if param == nil {
		return nil, false
	}

	val, ok := param.Value["value"]

	return val, ok
}

// ValidateLocalLoginParameter refuses to store auth.local_login.enabled=false
// while no SSO provider is available. Any other value is accepted.
func ValidateLocalLoginParameter(cfg *config.Config, value any) error {
	if parseBool(value, true) {
		return nil
	}

	if cfg == nil || len(cfg.SSOProviders()) == 0 {
		return ErrNoSSOProvider
	}

	return nil
}

// WarnLocalLoginFailOpen logs, at boot, a local login configured off that the
// server keeps on because no SSO provider is available.
func WarnLocalLoginFailOpen(ctx context.Context, store SystemParameterReader, cfg *config.Config) LocalLoginState {
	state := ResolveLocalLogin(ctx, store, cfg)
	if state.FailedOpen() {
		slog.WarnContext(ctx, "Local login is configured off (auth.local_login.enabled=false) "+
			"but no SSO provider is configured and enabled: keeping local login ON so the instance "+
			"stays reachable. Configure an SSO provider to enforce it.")
	}

	return state
}
