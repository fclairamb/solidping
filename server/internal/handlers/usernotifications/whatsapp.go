package usernotifications

import (
	"context"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/integrations/whatsapp"
)

// WhatsAppCodeSender delivers a verification code over WhatsApp. It exists as
// an interface so tests can inject a fake **per service instance** rather than
// swapping a package-level function, which two parallel tests would race on.
type WhatsAppCodeSender interface {
	SendVerificationCode(ctx context.Context, to, code string) error
}

// whatsAppTemplateSender is the production implementation: it sends the
// instance's Meta-approved *authentication* template. Meta forces the one-tap
// copy-code button format on that category, so the code is passed both as the
// body variable and as the button parameter.
type whatsAppTemplateSender struct {
	cfg config.WhatsAppConfig
	// baseURL overrides the Graph API base. Empty in production.
	baseURL string
}

// NewWhatsAppTemplateSender builds the production WhatsApp code sender.
func NewWhatsAppTemplateSender(cfg config.WhatsAppConfig) WhatsAppCodeSender {
	return &whatsAppTemplateSender{cfg: cfg}
}

// NewWhatsAppTemplateSenderWithBaseURL builds a sender pointed at a custom
// Graph API base — the httptest seam used by tests and by test-mode runs.
func NewWhatsAppTemplateSenderWithBaseURL(cfg config.WhatsAppConfig, baseURL string) WhatsAppCodeSender {
	return &whatsAppTemplateSender{cfg: cfg, baseURL: baseURL}
}

// SendVerificationCode sends the authentication template carrying the code.
func (s *whatsAppTemplateSender) SendVerificationCode(ctx context.Context, to, code string) error {
	if !s.cfg.Active() {
		return ErrNoWhatsAppProvider
	}

	baseURL := s.baseURL
	if baseURL == "" {
		baseURL = s.cfg.BaseURL
	}

	client, err := whatsapp.NewClient(whatsapp.Options{
		BaseURL:       baseURL,
		APIVersion:    s.cfg.ResolvedAPIVersion(),
		PhoneNumberID: s.cfg.PhoneNumberID,
		AccessToken:   s.cfg.AccessToken,
	})
	if err != nil {
		return ErrNoWhatsAppProvider
	}

	if _, err := client.SendTemplate(ctx, whatsapp.TemplateMessage{
		To:          to,
		Template:    s.cfg.ResolvedVerifyTemplate(),
		Language:    s.cfg.ResolvedTemplateLanguage(),
		BodyParams:  []string{code},
		ButtonParam: code,
	}); err != nil {
		return fmt.Errorf("send whatsapp verification template: %w", err)
	}

	return nil
}

// whatsAppCodeSender resolves the configured sender, preferring an explicitly
// injected one (tests, test mode) over the config-derived production sender.
func (s *Service) whatsAppCodeSender() (WhatsAppCodeSender, error) {
	if s.whatsAppSender != nil {
		return s.whatsAppSender, nil
	}

	if !s.whatsAppCfg.Active() {
		return nil, ErrNoWhatsAppProvider
	}

	return NewWhatsAppTemplateSender(s.whatsAppCfg), nil
}
