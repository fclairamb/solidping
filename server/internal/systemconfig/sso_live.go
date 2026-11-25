package systemconfig

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ssoParameterPrefix is the namespace of every system parameter whose overlay
// writes a sign-in provider config (auth.google.*, auth.oidc.*, auth.saml.*…).
const ssoParameterPrefix = "auth."

// SystemParameterLister is the slice of db.Service the SSO resolution needs.
type SystemParameterLister interface {
	ListSystemParameters(ctx context.Context) ([]*models.Parameter, error)
}

// IsSSOParameterKey reports whether saving or deleting key may change the
// sign-in provider configs, so the live SSO snapshot must be re-resolved.
func IsSSOParameterKey(key string) bool {
	return strings.HasPrefix(key, ssoParameterPrefix)
}

// ResolveSSOConfig rebuilds the SSO provider configs with the same precedence
// as Service.Initialize: env > DB > base (the startup config). It works on a
// scratch config, so the running one is never mutated in place.
func ResolveSSOConfig(
	ctx context.Context, store SystemParameterLister, base *config.SSOConfig,
) (*config.SSOConfig, error) {
	params, err := store.ListSystemParameters(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load system parameters: %w", err)
	}

	paramMap := make(map[string]any, len(params))
	for _, p := range params {
		if val, ok := p.Value["value"]; ok {
			paramMap[p.Key] = val
		}
	}

	tmp := &config.Config{}
	tmp.ApplySSO(base)

	known := getKnownParameters()
	for i := range known {
		def := &known[i]
		if !IsSSOParameterKey(string(def.Key)) {
			continue
		}

		if envVal := os.Getenv(def.EnvVar); envVal != "" {
			def.ApplyFunc(tmp, envVal)

			continue
		}

		if dbVal, ok := paramMap[string(def.Key)]; ok {
			def.ApplyFunc(tmp, dbVal)
		}
	}

	out := tmp.SSOFields()

	return &out, nil
}

// RefreshSSOConfig re-resolves the SSO provider configs of cfg and publishes
// them atomically, so a provider saved on Server Settings > Authentication is
// live without a restart (spec 2026-11-25-02). It enables live SSO on cfg when
// boot did not (tests building a bare config).
func RefreshSSOConfig(ctx context.Context, store SystemParameterLister, cfg *config.Config) error {
	if cfg == nil || store == nil {
		return nil
	}

	cfg.EnableLiveSSO()

	base := cfg.SSOBase()

	snapshot, err := ResolveSSOConfig(ctx, store, &base)
	if err != nil {
		return err
	}

	cfg.PublishSSO(snapshot)

	return nil
}

// InstallSSOReloader makes cfg re-resolve its SSO snapshot from store once it
// is older than config.SSOConfigRefreshTTL. This is how the other instances
// of a multi-instance deployment pick up a provider saved on one of them.
func InstallSSOReloader(ctx context.Context, cfg *config.Config, store SystemParameterLister) {
	if cfg == nil || store == nil {
		return
	}

	cfg.EnableLiveSSO()
	cfg.SetSSOReloader(ctx, func(ctx context.Context) (*config.SSOConfig, error) {
		base := cfg.SSOBase()

		return ResolveSSOConfig(ctx, store, &base)
	})
}
