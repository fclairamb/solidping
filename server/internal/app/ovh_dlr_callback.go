package app

import (
	"context"
	"log/slog"
	"net/url"
	"strings"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/ovhsmscb"
	smssvc "github.com/fclairamb/solidping/server/internal/integrations/sms"
)

// ovhDLRToken is the one place the delivery-receipt path token is resolved. It
// is deliberately not cached: both callers run after the system-config overlay.
func (s *Server) ovhDLRToken() string {
	return ovhsmscb.ResolveToken(s.config)
}

// ovhBaseURLReachable reports whether baseURL can be called from OVH: not
// empty, not the localhost default and not a loopback host.
func ovhBaseURLReachable(baseURL string) bool {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" || strings.TrimRight(baseURL, "/") == config.DefaultBaseURL {
		return false
	}

	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Hostname() == "" {
		return false
	}

	switch parsed.Hostname() {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return false
	}

	return true
}

// configureOVHDeliveryCallback registers the delivery-receipt URL with OVH. It
// runs at the end of InitializeSystemConfig, once cfg.Auth.JWTSecret and
// cfg.Server.BaseURL are final. Best-effort: a failure is one WARN. Logs one
// line (never the token) when the instance sender is OVH.
func (s *Server) configureOVHDeliveryCallback(ctx context.Context) {
	if !smssvc.IsOVHSender(s.instanceSMSSender) {
		return
	}

	tokenSource := "derived"
	if strings.TrimSpace(s.config.SMS.OVH.DLRToken) != "" {
		tokenSource = "explicit"
	}

	token := s.ovhDLRToken()
	if token == "" {
		slog.WarnContext(ctx, "ovh sms delivery receipts disabled: no token, "+
			"set SP_SMS_OVH_DLR_TOKEN or a non-default auth.jwt_secret")

		return
	}

	if !ovhBaseURLReachable(s.config.Server.BaseURL) {
		slog.WarnContext(ctx, "ovh sms delivery receipts not registered: the base URL is not public, "+
			"set SP_BASE_URL", "tokenSource", tokenSource)

		return
	}

	err := smssvc.ConfigureOVHDeliveryCallback(
		ctx, s.instanceSMSSender, ovhsmscb.CallbackURL(s.config.Server.BaseURL, token),
	)
	if err != nil {
		slog.WarnContext(ctx, "ovh sms delivery receipts not registered; "+
			"set the callback manually on the SMS account", "tokenSource", tokenSource, "error", err)

		return
	}

	slog.InfoContext(ctx, "ovh sms delivery receipts enabled",
		"tokenSource", tokenSource, "callbackRegistered", true)
}
