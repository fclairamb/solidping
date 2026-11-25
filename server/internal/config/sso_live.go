package config

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// SSOConfigRefreshTTL is how long a published SSO snapshot is trusted before
// the next read re-resolves it from env > DB > startup config in the
// background (spec 2026-11-25-02). The instance that saved a change publishes
// it at once; the TTL is what lets the OTHER instances of a multi-instance
// deployment pick it up, since no in-process notification reaches them.
const SSOConfigRefreshTTL = 30 * time.Second

// ssoReloadTimeout bounds one background re-resolution.
const ssoReloadTimeout = 10 * time.Second

// SSOConfig is the part of the configuration a super admin edits on Server
// Settings > Authentication: the federated sign-in providers. It is published
// as an immutable snapshot (Config.SSO) so a login request never reads a
// provider config while a save rewrites it.
type SSOConfig struct {
	Slack     SlackConfig
	Google    GoogleOAuthConfig
	GitHub    GitHubOAuthConfig
	Microsoft MicrosoftOAuthConfig
	GitLab    GitLabOAuthConfig
	Discord   DiscordOAuthConfig
	OIDC      OIDCOAuthConfig
	SAML      SAMLConfig
}

// SSOReloader re-resolves the SSO snapshot from its sources.
type SSOReloader func(ctx context.Context) (*SSOConfig, error)

// ssoState holds the live SSO snapshot of a Config.
type ssoState struct {
	// base is the startup (pre-overlay) SSO config: what a provider falls
	// back to once its DB parameters are deleted.
	base      SSOConfig
	current   atomic.Pointer[SSOConfig]
	loadedAt  atomic.Int64
	reloader  atomic.Pointer[SSOReloader]
	reloading atomic.Bool
}

// ssoFields copies the SSO part of the boot struct.
func (c *Config) ssoFields() SSOConfig {
	return SSOConfig{
		Slack:     c.Slack,
		Google:    c.Google,
		GitHub:    c.GitHub,
		Microsoft: c.Microsoft,
		GitLab:    c.GitLab,
		Discord:   c.Discord,
		OIDC:      c.OIDC,
		SAML:      c.SAML,
	}
}

// ApplySSO writes snapshot into the SSO fields of c. Used to build a scratch
// config that the system-parameter overlays can be applied to.
func (c *Config) ApplySSO(snapshot *SSOConfig) {
	c.Slack = snapshot.Slack
	c.Google = snapshot.Google
	c.GitHub = snapshot.GitHub
	c.Microsoft = snapshot.Microsoft
	c.GitLab = snapshot.GitLab
	c.Discord = snapshot.Discord
	c.OIDC = snapshot.OIDC
	c.SAML = snapshot.SAML
}

// SSOFields returns a copy of the SSO part of the boot struct.
func (c *Config) SSOFields() SSOConfig {
	return c.ssoFields()
}

// EnableLiveSSO switches c to the live SSO snapshot, capturing the current
// SSO fields as the startup base. It must run before c is shared between
// goroutines (systemconfig.Service.Initialize calls it at boot, before the
// overlay). Calling it again is a no-op.
func (c *Config) EnableLiveSSO() {
	if c.sso != nil {
		return
	}

	c.sso = &ssoState{base: c.ssoFields()}
}

// LiveSSOEnabled reports whether EnableLiveSSO ran.
func (c *Config) LiveSSOEnabled() bool {
	return c.sso != nil
}

// SSOBase returns the startup SSO config captured by EnableLiveSSO, or the
// current SSO fields when live SSO is off.
func (c *Config) SSOBase() SSOConfig {
	if c.sso == nil {
		return c.ssoFields()
	}

	return c.sso.base
}

// PublishSSO atomically replaces the live SSO snapshot. The snapshot must not
// be mutated afterwards. A no-op until EnableLiveSSO ran.
func (c *Config) PublishSSO(snapshot *SSOConfig) {
	if c.sso == nil || snapshot == nil {
		return
	}

	c.sso.current.Store(snapshot)
	c.sso.loadedAt.Store(time.Now().UnixNano())
}

// SetSSOReloader installs the function the TTL refresh calls. Nil disables it.
func (c *Config) SetSSOReloader(reload SSOReloader) {
	if c.sso == nil {
		return
	}

	if reload == nil {
		c.sso.reloader.Store(nil)

		return
	}

	c.sso.reloader.Store(&reload)
}

// SSO returns the SSO provider configs every login path must read. With live
// SSO enabled it is the last published snapshot, refreshed in the background
// once older than SSOConfigRefreshTTL; otherwise (tests, CLI) a copy of the
// boot struct. Callers must treat the result as read-only and take it once per
// operation so all their reads come from the same snapshot.
func (c *Config) SSO() *SSOConfig {
	state := c.sso
	if state == nil {
		snapshot := c.ssoFields()

		return &snapshot
	}

	snapshot := state.current.Load()
	if snapshot == nil {
		fields := c.ssoFields()

		return &fields
	}

	if time.Since(time.Unix(0, state.loadedAt.Load())) > SSOConfigRefreshTTL {
		c.refreshSSOInBackground()
	}

	return snapshot
}

// refreshSSOInBackground starts at most one background re-resolution.
func (c *Config) refreshSSOInBackground() {
	state := c.sso

	reload := state.reloader.Load()
	if reload == nil || !state.reloading.CompareAndSwap(false, true) {
		return
	}

	go func() {
		defer state.reloading.Store(false)

		ctx, cancel := context.WithTimeout(context.Background(), ssoReloadTimeout)
		defer cancel()

		snapshot, err := (*reload)(ctx)
		if err != nil {
			// Keep serving the last snapshot; retry after the next TTL.
			state.loadedAt.Store(time.Now().UnixNano())
			slog.WarnContext(ctx, "Failed to refresh the SSO provider config, keeping the previous one",
				"error", err)

			return
		}

		c.PublishSSO(snapshot)
	}()
}

// Sign-in provider identifiers, as used in the /auth/<provider> routes.
const (
	SSOProviderSlack     = "slack"
	SSOProviderGoogle    = "google"
	SSOProviderGitHub    = "github"
	SSOProviderMicrosoft = "microsoft"
	SSOProviderGitLab    = "gitlab"
	SSOProviderDiscord   = "discord"
	SSOProviderOIDC      = "oidc"
	SSOProviderSAML      = "saml"
)

// RoutesActive reports whether the /auth/<provider> routes of provider serve
// requests in this snapshot. The routes are always mounted (spec
// 2026-11-25-02) and answer 404 while this is false. SAML only needs to be
// enabled: its SP metadata is served before an IdP is configured (login and
// ACS check the IdP side again). OIDC requires the full Configured() set,
// client secret included.
func (c *SSOConfig) RoutesActive(provider string) bool {
	switch provider {
	case SSOProviderSlack:
		return c.Slack.Enabled && c.Slack.ClientID != ""
	case SSOProviderGoogle:
		return c.Google.Enabled && c.Google.ClientID != ""
	case SSOProviderGitHub:
		return c.GitHub.Enabled && c.GitHub.ClientID != ""
	case SSOProviderMicrosoft:
		return c.Microsoft.Enabled && c.Microsoft.ClientID != ""
	case SSOProviderGitLab:
		return c.GitLab.Enabled && c.GitLab.ClientID != ""
	case SSOProviderDiscord:
		return c.Discord.Enabled && c.Discord.ClientID != ""
	case SSOProviderOIDC:
		return c.OIDC.Configured()
	case SSOProviderSAML:
		return c.SAML.Enabled
	default:
		return false
	}
}
