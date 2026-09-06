// Package publicconfig serves GET /api/v1/config: the unauthenticated,
// browser-safe configuration blob the dashboard reads at boot, before any
// login has happened.
//
// It is deliberately a *general-purpose* document rather than a
// PostHog-specific endpoint — future public feature flags join the same JSON
// object instead of minting an endpoint each.
//
// Hard rule: nothing marked Secret in the system-parameter registry may ever
// appear here, and nothing that is off may leak its configuration. In
// particular posthog.personal_api_key is never read by this package, and when
// PostHog is inactive the projectApiKey/host fields are OMITTED entirely
// (json omitempty on pointer/empty values), not emitted as empty strings — a
// self-hosted operator must be able to see at a glance that the feature is
// wholly unconfigured.
package publicconfig

import (
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// PostHogPublicConfig is the browser-safe view of the PostHog settings.
//
// Enabled is the resolved enablement rule (config.PostHogConfig.Active), NOT
// the raw posthog.enabled kill switch: a deployment with the switch on but no
// project key reports enabled=false, because nothing will in fact be captured.
// ProjectAPIKey and Host are present if and only if Enabled is true.
type PostHogPublicConfig struct {
	Enabled bool `json:"enabled"`
	// ProjectAPIKey is the public phc_… browser key. Absent when disabled.
	ProjectAPIKey string `json:"projectApiKey,omitempty"`
	// Host is the api_host the dashboard posts events to. It defaults to the
	// first-party proxy path (see config.PostHogProxyPath). Absent when disabled.
	Host string `json:"host,omitempty"`
	// UIHost is posthog-js ui_host, present only when Host is the first-party
	// proxy path so toolbar and "view in PostHog" links still resolve to the
	// PostHog app. Absent when disabled or when an operator set an explicit host.
	UIHost string `json:"uiHost,omitempty"`
}

// WhatsAppPublicConfig is the browser-safe view of the instance's WhatsApp
// capability. It is a single boolean by design: the dashboard only needs to
// know whether to offer the WhatsApp contact type and severity channel. No
// credential, phone-number id, WABA id or template name is ever exposed —
// those are operator-side facts a browser has no business learning.
//
// Enabled is the resolved config.WhatsAppConfig.Active() rule, NOT the raw
// whatsapp.enabled kill switch: an instance with the switch on but no access
// token reports enabled=false, because nothing would in fact be delivered.
type WhatsAppPublicConfig struct {
	Enabled bool `json:"enabled"`
}

// TelegramPublicConfig is the browser-safe view of the instance's Telegram
// capability.
//
// Enabled is the resolved config.TelegramConfig.Active() rule, NOT the raw
// telegram.enabled kill switch. BotUsername is emitted ONLY when enabled
// (omitempty): it is not a secret — anyone can find the bot in Telegram — but
// an operator must be able to see at a glance that an unconfigured instance
// emits nothing at all. The bot *token* and the webhook secret never appear
// here under any circumstance.
type TelegramPublicConfig struct {
	Enabled bool `json:"enabled"`
	// BotUsername is the bot's @username without the '@', which the dashboard
	// needs to build the `https://t.me/<botUsername>?start=<token>` deep link.
	BotUsername string `json:"botUsername,omitempty"`
}

// SMSPublicConfig is the browser-safe view of the instance's SERVER-PROVIDED
// SMS and voice capability — the mode an org gets when it has not brought its
// own provider account.
//
// Enabled / VoiceEnabled are the resolved config.SMSConfig.Active() /
// config.VoiceConfig.Active() rules, NOT the raw kill switches: an instance
// with the switch on but no credentials reports false, because nothing would
// in fact be delivered.
//
// Sender and Provider are emitted only when enabled. Neither is a secret:
// the sender is the string every recipient's handset displays, and the
// provider name is an operator-side fact the dashboard needs to explain what
// "server-provided" means on this deployment. No key, token, consumer key or
// service name ever appears here under any circumstance.
type SMSPublicConfig struct {
	Enabled bool `json:"enabled"`
	// Sender is the instance-wide sender identity (E.164 or an alphanumeric
	// sender). There is deliberately no per-org override on this path.
	Sender string `json:"sender,omitempty"`
	// Provider is "twilio" or "ovh".
	Provider string `json:"provider,omitempty"`
	// VoiceEnabled reports the instance-level voice capability, resolved
	// INDEPENDENTLY of SMS: OVH has no voice API, so an instance can perfectly
	// well offer OVH SMS and Twilio voice at the same time — or SMS and no
	// voice at all.
	VoiceEnabled bool `json:"voiceEnabled"`
}

// DemoPublicConfig advertises the shared public live demo (spec 2026-09-06-02)
// so the login page can offer a one-click entry.
//
// The PASSWORD IS DELIBERATELY IN HERE. That looks alarming and is not: the
// demo credential is public by design — the whole feature is "anyone can log
// in and look around" — and the alternative is hardcoding the same string into
// the JavaScript bundle, where it would be just as public but would also drift
// from the server's own configuration the moment an operator changed
// SP_DEMO_PASSWORD. Serving it explicitly keeps one source of truth.
//
// Nothing is emitted at all when the demo is off, so a self-hosted install
// that never turns it on advertises nothing.
type DemoPublicConfig struct {
	Enabled  bool   `json:"enabled"`
	OrgSlug  string `json:"orgSlug,omitempty"`
	Email    string `json:"email,omitempty"`
	Password string `json:"password,omitempty"`
}

// Response is the public config document. Fields are added here as new public
// flags appear; every one of them must be non-secret and browser-safe.
type Response struct {
	PostHog  PostHogPublicConfig  `json:"posthog"`
	WhatsApp WhatsAppPublicConfig `json:"whatsapp"`
	Telegram TelegramPublicConfig `json:"telegram"`
	SMS      SMSPublicConfig      `json:"sms"`
	Demo     DemoPublicConfig     `json:"demo"`
}

// Handler serves the public config document.
type Handler struct {
	base.HandlerBase
	cfg *config.Config
}

// NewHandler creates a new public config handler.
func NewHandler(cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		cfg:         cfg,
	}
}

// Build assembles the public config document from the (already overlaid)
// server config. Exported so tests can assert the shape without an HTTP round
// trip, and so future callers can embed the same document elsewhere.
func Build(cfg *config.Config) Response {
	resp := Response{}

	if cfg != nil && cfg.PostHog.Active() {
		resp.PostHog = PostHogPublicConfig{
			Enabled:       true,
			ProjectAPIKey: cfg.PostHog.ProjectAPIKey,
			Host:          cfg.PostHog.BrowserAPIHost(),
			UIHost:        cfg.PostHog.BrowserUIHost(),
		}
	}

	if cfg != nil && cfg.WhatsApp.Active() {
		resp.WhatsApp = WhatsAppPublicConfig{Enabled: true}
	}

	if cfg != nil && cfg.Telegram.Active() {
		resp.Telegram = TelegramPublicConfig{
			Enabled:     true,
			BotUsername: cfg.Telegram.ResolvedBotUsername(),
		}
	}

	if cfg != nil && cfg.Demo.Enabled {
		resp.Demo = DemoPublicConfig{
			Enabled:  true,
			OrgSlug:  cfg.Demo.ResolvedOrgSlug(),
			Email:    cfg.Demo.ResolvedEmail(),
			Password: cfg.Demo.ResolvedPassword(),
		}
	}

	if cfg != nil {
		resp.SMS.VoiceEnabled = cfg.Voice.Active()

		if cfg.SMS.Active() {
			resp.SMS.Enabled = true
			resp.SMS.Sender = cfg.SMS.Sender
			resp.SMS.Provider = cfg.SMS.ResolvedProvider()
		}
	}

	return resp
}

// GetConfig handles GET /api/v1/config (public, no authentication).
func (h *Handler) GetConfig(writer http.ResponseWriter, _ *http.Request) error {
	return h.WriteJSON(writer, http.StatusOK, Build(h.cfg))
}
