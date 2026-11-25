package app

import (
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// mountSSORoutes mounts the /auth/<provider> routes of every federated
// sign-in provider, whether or not it is configured at boot (spec
// 2026-11-25-02). auth.RequireSSOProvider reads the live SSO snapshot on each
// request: a provider saved on Server Settings > Authentication answers at
// once, and one not configured (or switched off) answers 404.
func (s *Server) mountSSORoutes(api *httpx.Group) {
	ssoGroup := func(provider string) *httpx.Group {
		return api.NewGroup("/auth/" + provider).Use(auth.RequireSSOProvider(s.config, provider))
	}

	// Slack OAuth routes (org-independent)
	slackHandler := auth.NewSlackOAuthHandler(
		auth.NewSlackOAuthService(s.dbService, s.config, s.authService), s.config)
	slackAuth := ssoGroup(config.SSOProviderSlack)
	slackAuth.GET("/login", slackHandler.Login)
	slackAuth.GET("/callback", slackHandler.Callback)
	// TODO(remove after next release): spec 2026-09-25-12
	// oauth-callback-one-time-code-exchange. Slack installs now hand off
	// through /auth/handoff/exchange; this only redeems codes an older
	// pod minted during the rolling deploy.
	slackAuth.POST("/exchange", slackHandler.Exchange)

	// Google, GitHub, Microsoft, GitLab and Discord OAuth routes (org-scoped)
	googleHandler := auth.NewGoogleOAuthHandler(
		auth.NewGoogleOAuthService(s.dbService, s.config, s.authService), s.config)
	googleAuth := ssoGroup(config.SSOProviderGoogle)
	googleAuth.GET("/login", googleHandler.Login)
	googleAuth.GET("/callback", googleHandler.Callback)

	gitHubHandler := auth.NewGitHubOAuthHandler(
		auth.NewGitHubOAuthService(s.dbService, s.config, s.authService), s.config)
	gitHubAuth := ssoGroup(config.SSOProviderGitHub)
	gitHubAuth.GET("/login", gitHubHandler.Login)
	gitHubAuth.GET("/callback", gitHubHandler.Callback)

	microsoftHandler := auth.NewMicrosoftOAuthHandler(
		auth.NewMicrosoftOAuthService(s.dbService, s.config, s.authService), s.config)
	microsoftAuth := ssoGroup(config.SSOProviderMicrosoft)
	microsoftAuth.GET("/login", microsoftHandler.Login)
	microsoftAuth.GET("/callback", microsoftHandler.Callback)

	gitLabHandler := auth.NewGitLabOAuthHandler(
		auth.NewGitLabOAuthService(s.dbService, s.config, s.authService), s.config)
	gitLabAuth := ssoGroup(config.SSOProviderGitLab)
	gitLabAuth.GET("/login", gitLabHandler.Login)
	gitLabAuth.GET("/callback", gitLabHandler.Callback)

	discordHandler := auth.NewDiscordOAuthHandler(
		auth.NewDiscordOAuthService(s.dbService, s.config, s.authService), s.config)
	discordAuth := ssoGroup(config.SSOProviderDiscord)
	discordAuth.GET("/login", discordHandler.Login)
	discordAuth.GET("/callback", discordHandler.Callback)

	// Generic OIDC routes (org-scoped): live once Configured(), client secret
	// included.
	oidcHandler := auth.NewOIDCOAuthHandler(
		auth.NewOIDCOAuthService(s.dbService, s.config, s.authService), s.config)
	oidcAuth := ssoGroup(config.SSOProviderOIDC)
	oidcAuth.GET("/login", oidcHandler.Login)
	oidcAuth.GET("/callback", oidcHandler.Callback)

	// SAML 2.0 SP routes (org-scoped). Metadata is served whenever SAML is
	// enabled at all — even before an IdP is configured — since an admin
	// typically needs this SP's own metadata first, to configure their IdP,
	// before they have IdP metadata to paste back; login/ACS need the IdP
	// side configured too (checked again inside the service).
	samlHandler := auth.NewSAMLHandler(auth.NewSAMLService(s.dbService, s.config, s.authService), s.config)
	samlAuth := ssoGroup(config.SSOProviderSAML)
	samlAuth.GET("/login", samlHandler.Login)
	samlAuth.POST("/acs", samlHandler.ACS)
	samlAuth.GET("/metadata", samlHandler.Metadata)
}
