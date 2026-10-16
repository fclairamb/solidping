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

// ovhDLRToken is the single source of the OVH delivery-receipt path token,
// used both for the URL registered with OVH and for the handler. It must only
// be called after InitializeSystemConfig, and is deliberately not cached.
func (s *Server) ovhDLRToken() string {
	return ovhsmscb.ResolveToken(s.config)
}

// publicBaseURL reports whether the base URL can be reached by OVH: not the
// localhost default and not a loopback host.
func publicBaseURL(baseURL string) bool {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" || trimmed == config.DefaultBaseURL {
		return false
	}

	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Hostname() == "" {
		return false
	}

	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || host == "::1" || strings.HasPrefix(host, "127.") {
		return false
	}

	return true
}

// configureOVHDeliveryCallback registers the account-level OVH `callBack`
// once the config overlay is final. Best effort: failures are one WARN. It
// logs exactly one line when the instance sender is OVH, never the token.
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
		slog.WarnContext(ctx, "ovh sms delivery receipts disabled: no DLR token "+
			"(set SP_SMS_OVH_DLR_TOKEN or a non-default auth.jwt_secret)")

		return
	}

	if !publicBaseURL(s.config.Server.BaseURL) {
		slog.WarnContext(ctx, "ovh sms delivery receipts callback not registered: "+
			"set SP_BASE_URL to the public URL of this instance",
			"tokenSource", tokenSource, "callbackRegistered", false)

		return
	}

	err := smssvc.ConfigureOVHDeliveryCallback(
		ctx, s.instanceSMSSender, ovhsmscb.CallbackURL(s.config.Server.BaseURL, token))
	if err != nil {
		slog.WarnContext(ctx, "could not register the OVH SMS delivery callback; "+
			"set it manually on the SMS account if you want delivery receipts",
			"tokenSource", tokenSource, "callbackRegistered", false, "error", err)

		return
	}

	slog.InfoContext(ctx, "ovh sms delivery receipts enabled",
		"tokenSource", tokenSource, "callbackRegistered", true)
}
