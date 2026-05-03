package email

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"

	"github.com/wneessen/go-mail"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/version"
)

// defaultFromName is used as the display name in the From header when the
// operator has not set one explicitly. Mail clients render bare addresses
// awkwardly, so we always send a name.
const defaultFromName = "SolidPing"

// ErrNoRecipients is returned when no recipients are specified.
var ErrNoRecipients = errors.New("no recipients specified")

// SMTPSender implements the Sender interface using SMTP.
type SMTPSender struct {
	config *config.EmailConfig
	logger *slog.Logger
}

// NewSender creates a new SMTP sender.
func NewSender(cfg *config.EmailConfig, logger *slog.Logger) *SMTPSender {
	return &SMTPSender{
		config: cfg,
		logger: logger,
	}
}

// SendResult contains the result of sending an email.
type SendResult struct {
	Sent    bool   `json:"sent"`
	Message string `json:"message"`
}

// Send delivers an email. Returns nil immediately if email is disabled (no-op).
func (s *SMTPSender) Send(ctx context.Context, msg *Message) (*SendResult, error) {
	if !s.config.Enabled {
		s.logger.DebugContext(ctx, "email sending disabled, skipping", "subject", msg.Subject)

		return &SendResult{Sent: false, Message: "email sending disabled"}, nil
	}

	if len(msg.Recipients.To) == 0 {
		return nil, ErrNoRecipients
	}

	mailMsg, err := s.buildMessage(msg)
	if err != nil {
		return nil, err
	}

	return s.sendMessage(ctx, mailMsg, msg)
}

// buildMessage creates a go-mail message from our Message struct.
func (s *SMTPSender) buildMessage(msg *Message) (*mail.Msg, error) {
	mailMsg := mail.NewMsg()

	// Identify ourselves; otherwise go-mail stamps "go-mail vX.Y.Z" as X-Mailer.
	mailMsg.SetGenHeader(mail.HeaderXMailer, "SolidPing/"+version.Version)

	if err := s.setFrom(mailMsg); err != nil {
		return nil, err
	}

	if err := s.setRecipients(mailMsg, &msg.Recipients); err != nil {
		return nil, err
	}

	mailMsg.Subject(msg.Subject)
	s.setBody(mailMsg, msg)

	return mailMsg, nil
}

// setFrom sets the sender address on the message, defaulting the display
// name when the operator has not configured one.
func (s *SMTPSender) setFrom(mailMsg *mail.Msg) error {
	name := s.config.FromName
	if name == "" {
		name = defaultFromName
	}

	if err := mailMsg.FromFormat(name, s.config.From); err != nil {
		return fmt.Errorf("setting from address: %w", err)
	}

	return nil
}

// setRecipients sets the To, CC, BCC, and ReplyTo addresses on the message.
func (s *SMTPSender) setRecipients(mailMsg *mail.Msg, recipients *Recipients) error {
	if err := mailMsg.To(recipients.To...); err != nil {
		return fmt.Errorf("setting to addresses: %w", err)
	}

	if len(recipients.CC) > 0 {
		if err := mailMsg.Cc(recipients.CC...); err != nil {
			return fmt.Errorf("setting cc addresses: %w", err)
		}
	}

	if len(recipients.BCC) > 0 {
		if err := mailMsg.Bcc(recipients.BCC...); err != nil {
			return fmt.Errorf("setting bcc addresses: %w", err)
		}
	}

	if recipients.ReplyTo != "" {
		if err := mailMsg.ReplyTo(recipients.ReplyTo); err != nil {
			return fmt.Errorf("setting reply-to address: %w", err)
		}
	}

	return nil
}

// setBody sets the plaintext and/or HTML body on the message.
//
// Per RFC 2046 §5.1.4, multipart/alternative parts must be ordered from
// least to most preferred (preferred last). Spec-compliant readers
// (Gmail, Apple Mail) pick the LAST part they can render, so plaintext
// goes first and HTML goes last — otherwise Gmail renders our auto-text
// (which lynx-renders the wrapper tables in base.html) instead of our
// styled HTML.
func (s *SMTPSender) setBody(mailMsg *mail.Msg, msg *Message) {
	switch {
	case msg.HTML != "" && msg.Text != "":
		mailMsg.SetBodyString(mail.TypeTextPlain, msg.Text)
		mailMsg.AddAlternativeString(mail.TypeTextHTML, msg.HTML)
	case msg.HTML != "":
		mailMsg.SetBodyString(mail.TypeTextHTML, msg.HTML)
	case msg.Text != "":
		mailMsg.SetBodyString(mail.TypeTextPlain, msg.Text)
	}
}

// getAuthType returns the SMTP auth type based on config.
// Returns nil for "noauth" to skip authentication.
func (s *SMTPSender) getAuthType() mail.SMTPAuthType {
	switch s.config.AuthType {
	case "plain":
		return mail.SMTPAuthPlain
	case "cram-md5":
		return mail.SMTPAuthCramMD5
	case "noauth":
		return mail.SMTPAuthNoAuth
	case "login", "":
		return mail.SMTPAuthLogin
	default:
		return mail.SMTPAuthLogin
	}
}

// sendMessage creates an SMTP client and sends the message.
func (s *SMTPSender) sendMessage(ctx context.Context, mailMsg *mail.Msg, msg *Message) (*SendResult, error) {
	opts := []mail.Option{
		mail.WithPort(s.config.Port),
		mail.WithSMTPAuth(s.getAuthType()),
		mail.WithUsername(s.config.Username),
		mail.WithPassword(s.config.Password),
	}

	// Implicit TLS (smtps, port 465 style) requires a TLS handshake on
	// connect. STARTTLS upgrades a plaintext connection. We default to
	// opportunistic STARTTLS for back-compat with existing configs that
	// leave protocol unset.
	switch s.config.Protocol {
	case "tls", "ssl", "smtps":
		opts = append(opts, mail.WithSSL())
	case "none", "plain":
		opts = append(opts, mail.WithTLSPortPolicy(mail.NoTLS))
	default:
		opts = append(opts, mail.WithTLSPortPolicy(mail.TLSOpportunistic))
	}

	if s.config.InsecureSkipVerify {
		opts = append(opts, mail.WithTLSConfig(&tls.Config{
			InsecureSkipVerify: true,
		}))
	}

	client, err := mail.NewClient(s.config.Host, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating mail client: %w", err)
	}

	s.logger.InfoContext(ctx, "sending email",
		"to", msg.Recipients.To,
		"subject", msg.Subject,
	)

	if err := client.DialAndSendWithContext(ctx, mailMsg); err != nil {
		s.logger.ErrorContext(ctx, "failed to send email",
			"to", msg.Recipients.To,
			"subject", msg.Subject,
			"error", err,
		)

		return nil, fmt.Errorf("sending email: %w", err)
	}

	s.logger.InfoContext(ctx, "email sent successfully",
		"to", msg.Recipients.To,
		"subject", msg.Subject,
	)

	return &SendResult{Sent: true, Message: "email sent successfully"}, nil
}
