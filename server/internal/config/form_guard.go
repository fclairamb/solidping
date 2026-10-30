package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Defaults for FormGuardConfig. Exported so the auth service and its tests
// cannot drift from the documented values.
const (
	// DefaultFormGuardMinFillTime is the youngest a form ticket may be when the
	// form is submitted. Nobody types a name, an email and a password in less;
	// the bot this guards against posted about a second after the page loaded.
	DefaultFormGuardMinFillTime = 3 * time.Second
	// DefaultFormGuardTicketTTL bounds how long a form ticket stays usable, so
	// a tab left open for a working day still submits.
	DefaultFormGuardTicketTTL = 24 * time.Hour
	// DefaultFormGuardPerIPPerHour bounds what one client IP can do per hour:
	// sign-ups still waiting for confirmation (a confirmed one frees its slot),
	// and password-reset requests. Over it the endpoint answers 429.
	DefaultFormGuardPerIPPerHour = 12
	// DefaultFormGuardPerEmailPerHour bounds the emails each form can send to
	// one address per hour. Over it the endpoint answers as on success and
	// sends nothing.
	DefaultFormGuardPerEmailPerHour = 3
)

// Env names of the form guard. Every key segment is snake_case, which koanf's
// env provider collapses to dots, so applyFormGuardEnv reads them by hand.
const (
	EnvFormGuardEnabled         = "SP_AUTH_FORM_GUARD_ENABLED"
	EnvFormGuardMinFillTime     = "SP_AUTH_FORM_GUARD_MIN_FILL_TIME"
	EnvFormGuardTicketTTL       = "SP_AUTH_FORM_GUARD_TICKET_TTL"
	EnvFormGuardPerIPPerHour    = "SP_AUTH_FORM_GUARD_PER_IP_PER_HOUR"
	EnvFormGuardPerEmailPerHour = "SP_AUTH_FORM_GUARD_PER_EMAIL_PER_HOUR"
)

// FormGuardConfig protects the two public forms that make the server email an
// address typed by an anonymous visitor: sign-up and password reset. Without
// it, a bot can use them to flood third-party mailboxes from the instance's
// mail domain ("subscription bombing").
//
// Enabled requires a server-signed form ticket (GET /api/v1/auth/form-ticket)
// at least MinFillTime old and an empty honeypot field. A submission that
// fails either check answers exactly like a success and sends nothing. The
// per-IP and per-email budgets apply whether Enabled is set or not; 0 turns a
// budget off. The zero value disables everything, which is what hand-built
// test configs get.
type FormGuardConfig struct {
	Enabled         bool          `koanf:"enabled"`
	MinFillTime     time.Duration `koanf:"min_fill_time"`
	TicketTTL       time.Duration `koanf:"ticket_ttl"`
	PerIPPerHour    int           `koanf:"per_ip_per_hour"`
	PerEmailPerHour int           `koanf:"per_email_per_hour"`
}

// DefaultFormGuardConfig returns the shipped guard: ticket and honeypot
// required, 3 s minimum fill time, 24 h tickets, 12 unconfirmed sign-ups or
// reset requests per IP and 3 emails per address, each per hour and per form.
func DefaultFormGuardConfig() FormGuardConfig {
	return FormGuardConfig{
		Enabled:         true,
		MinFillTime:     DefaultFormGuardMinFillTime,
		TicketTTL:       DefaultFormGuardTicketTTL,
		PerIPPerHour:    DefaultFormGuardPerIPPerHour,
		PerEmailPerHour: DefaultFormGuardPerEmailPerHour,
	}
}

// ResolvedTicketTTL is the ticket lifetime, DefaultFormGuardTicketTTL when unset.
func (c FormGuardConfig) ResolvedTicketTTL() time.Duration {
	if c.TicketTTL <= 0 {
		return DefaultFormGuardTicketTTL
	}

	return c.TicketTTL
}

// applyFormGuardEnv reads the SP_AUTH_FORM_GUARD_* variables. A malformed
// value is ignored and the configured one stays. In test run mode the minimum
// fill time drops to 0 unless SP_AUTH_FORM_GUARD_MIN_FILL_TIME is set: the
// Playwright suites fill a form in well under 3 s, and they still prove that
// the dashboard sends a valid ticket, which stays required.
func applyFormGuardEnv(cfg *FormGuardConfig, runMode string) {
	if v, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("SP_AUTH_FORM_GUARD_ENABLED"))); err == nil {
		cfg.Enabled = v
	}

	minFill := strings.TrimSpace(os.Getenv("SP_AUTH_FORM_GUARD_MIN_FILL_TIME"))
	if minFill == "" && runMode == runModeTest {
		cfg.MinFillTime = 0
	}

	readDuration := func(raw string, dst *time.Duration) {
		if d, err := time.ParseDuration(strings.TrimSpace(raw)); err == nil && d >= 0 {
			*dst = d
		}
	}

	readDuration(minFill, &cfg.MinFillTime)
	readDuration(os.Getenv("SP_AUTH_FORM_GUARD_TICKET_TTL"), &cfg.TicketTTL)

	readInt := func(raw string, dst *int) {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n >= 0 {
			*dst = n
		}
	}

	readInt(os.Getenv("SP_AUTH_FORM_GUARD_PER_IP_PER_HOUR"), &cfg.PerIPPerHour)
	readInt(os.Getenv("SP_AUTH_FORM_GUARD_PER_EMAIL_PER_HOUR"), &cfg.PerEmailPerHour)
}
