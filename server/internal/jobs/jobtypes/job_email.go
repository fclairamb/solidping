package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// Email job validation errors.
var (
	ErrEmailNoRecipients     = errors.New("email job requires at least one recipient in 'to'")
	ErrEmailNoSubject        = errors.New("email job requires a subject")
	ErrEmailNoContent        = errors.New("email job requires either content (html/text) or a template")
	ErrEmailContentConflict  = errors.New("email job cannot have both raw content and template")
	ErrEmailSenderMissing    = errors.New("email sender not configured")
	ErrEmailFormatterMissing = errors.New("email formatter not configured")
)

// EmailJobConfig configures email parameters.
type EmailJobConfig struct {
	// Recipients
	To      []string `json:"to"`                // Primary recipients (required)
	CC      []string `json:"cc,omitempty"`      // Carbon copy recipients
	BCC     []string `json:"bcc,omitempty"`     // Blind carbon copy recipients
	ReplyTo string   `json:"replyTo,omitempty"` // Reply-to address

	// Content (either raw content OR template)
	Subject string `json:"subject"`        // Email subject (required)
	HTML    string `json:"html,omitempty"` // Raw HTML content
	Text    string `json:"text,omitempty"` // Raw plain text content

	// Template-based content (alternative to raw HTML/Text)
	Template     string `json:"template,omitempty"`     // Template name (e.g., "incident.html")
	TemplateData any    `json:"templateData,omitempty"` // Data to pass to template
}

// EmailJobDefinition is the factory for email jobs.
type EmailJobDefinition struct{}

// Type returns the job type identifier.
func (d *EmailJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeEmail
}

// CreateJobRun creates a new email job run instance.
//
//nolint:ireturn // Factory pattern requires interface return
func (d *EmailJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg EmailJobConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, fmt.Errorf("parsing email config: %w", err)
	}

	if err := validateEmailConfig(&cfg); err != nil {
		return nil, err
	}

	return &EmailJobRun{config: cfg}, nil
}

func validateEmailConfig(cfg *EmailJobConfig) error {
	if len(cfg.To) == 0 {
		return ErrEmailNoRecipients
	}

	hasContent := cfg.HTML != "" || cfg.Text != ""
	hasTemplate := cfg.Template != ""

	if !hasContent && !hasTemplate {
		return ErrEmailNoContent
	}

	if hasContent && hasTemplate {
		return ErrEmailContentConflict
	}

	// Subject is required when sending raw content. With a template, the
	// template may define its own subject via {{define "subject"}}, so we
	// can't require Subject here without parsing the template — defer that
	// check to runtime if neither is supplied.
	if hasContent && cfg.Subject == "" {
		return ErrEmailNoSubject
	}

	return nil
}

// EmailJobRun is the executable instance of an email job.
type EmailJobRun struct {
	config EmailJobConfig
}

// Run executes the email job.
func (r *EmailJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	// Check if email sender is available
	if jctx.Services.EmailSender == nil {
		return ErrEmailSenderMissing
	}

	log.InfoContext(ctx, "Preparing email",
		"to", r.config.To,
		"subject", r.config.Subject,
		"template", r.config.Template,
	)

	// Build the email message
	msg, err := r.buildMessage(jctx)
	if err != nil {
		return fmt.Errorf("building email message: %w", err)
	}

	// Send the email
	log.InfoContext(ctx, "Sending email",
		"to", r.config.To,
		"subject", r.config.Subject,
	)

	result, err := jctx.Services.EmailSender.Send(ctx, msg)
	if err != nil {
		// Check if this is a network error (retryable)
		if isNetworkError(err) {
			log.WarnContext(ctx, "Email send failed due to network error, will retry",
				"error", err,
			)

			return jobdef.NewRetryableError(fmt.Errorf("network error sending email: %w", err))
		}

		return fmt.Errorf("sending email: %w", err)
	}

	// Store send result in job output
	output := models.JSONMap{
		"sent":    result.Sent,
		"message": result.Message,
	}
	if result.MessageID != "" {
		output["messageId"] = result.MessageID
	}

	jctx.Job.Output = output

	log.InfoContext(ctx, "Email sent successfully",
		"to", r.config.To,
		"subject", r.config.Subject,
	)

	return nil
}

// buildMessage creates an email.Message from the job config.
func (r *EmailJobRun) buildMessage(jctx *jobdef.JobContext) (*email.Message, error) {
	msg := &email.Message{
		Recipients: email.Recipients{
			To:      r.config.To,
			CC:      r.config.CC,
			BCC:     r.config.BCC,
			ReplyTo: r.config.ReplyTo,
		},
		Subject: r.config.Subject,
	}

	// Use template if specified
	if r.config.Template != "" {
		if jctx.Services.EmailFormatter == nil {
			return nil, ErrEmailFormatterMissing
		}

		subject, html, err := jctx.Services.EmailFormatter.Format(
			r.config.Template, r.config.TemplateData)
		if err != nil {
			return nil, fmt.Errorf("formatting template %s: %w", r.config.Template, err)
		}

		msg.HTML = html
		// Caller-supplied subject overrides the template's; otherwise use
		// what the template defined.
		if msg.Subject == "" {
			msg.Subject = subject
		}
	} else {
		// Use raw content
		msg.HTML = r.config.HTML
		msg.Text = r.config.Text
	}

	return msg, nil
}

// isNetworkError checks if an error is a network-related error.
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}

	// Check for net.Error
	var netErr net.Error
	if ok := errorAs(err, &netErr); ok {
		return true
	}

	// Check for common network error messages
	errStr := err.Error()
	networkIndicators := []string{
		"connection refused",
		"connection reset",
		"connection timed out",
		"no such host",
		"network is unreachable",
		"i/o timeout",
		"dial tcp",
		"dial udp",
		"dial failed",
	}

	for _, indicator := range networkIndicators {
		if strings.Contains(strings.ToLower(errStr), indicator) {
			return true
		}
	}

	return false
}

// errorAs is a helper to check error types (avoids import of errors package).
func errorAs(err error, target any) bool {
	// Simple implementation that checks the error chain
	for err != nil {
		if netErr, ok := target.(*net.Error); ok {
			if n, ok := err.(net.Error); ok { //nolint:errorlint // need direct type check
				*netErr = n
				return true
			}
		}

		// Try to unwrap
		if unwrapper, ok := err.(interface{ Unwrap() error }); ok {
			err = unwrapper.Unwrap()
		} else {
			break
		}
	}

	return false
}
