package config

import (
	"os"
	"strconv"
)

// Defaults for StatusSubscribersConfig (spec 2026-10-28-02). Exported so the
// subscribers service and its tests cannot drift from the documented values.
const (
	// DefaultStatusSubscriberConfirmPerRecipientPerHour bounds the confirm
	// mails one address receives, across every status page of the instance.
	DefaultStatusSubscriberConfirmPerRecipientPerHour = 3
	// DefaultStatusSubscriberConfirmPerPagePerHour bounds the confirm mails
	// one status page sends.
	DefaultStatusSubscriberConfirmPerPagePerHour = 100
	// DefaultStatusSubscriberConfirmPerIPPerHour bounds the confirm mails one
	// client IP (trusted-proxy resolution) can trigger.
	DefaultStatusSubscriberConfirmPerIPPerHour = 20
)

// Env names of the confirm-mail budgets. Their key segments are snake_case,
// which koanf's env provider collapses to dots, so applyStatusSubscribersEnv
// reads them by hand.
const (
	EnvStatusSubscriberConfirmPerRecipient = "SP_STATUS_SUBSCRIBERS_CONFIRM_PER_RECIPIENT_PER_HOUR"
	EnvStatusSubscriberConfirmPerPage      = "SP_STATUS_SUBSCRIBERS_CONFIRM_PER_PAGE_PER_HOUR"
	EnvStatusSubscriberConfirmPerIP        = "SP_STATUS_SUBSCRIBERS_CONFIRM_PER_IP_PER_HOUR"
)

// StatusSubscribersConfig budgets the double opt-in mail of the public
// status-page subscribe endpoint. Every budget is a count per sliding hour; 0
// disables that budget. Over budget the endpoint still answers 202 but sends
// nothing, so it is neither a mail cannon nor an oracle.
type StatusSubscribersConfig struct {
	ConfirmPerRecipientPerHour int `koanf:"confirm_per_recipient_per_hour"`
	ConfirmPerPagePerHour      int `koanf:"confirm_per_page_per_hour"`
	ConfirmPerIPPerHour        int `koanf:"confirm_per_ip_per_hour"`
}

// DefaultStatusSubscribersConfig returns the shipped budgets: 3 per recipient,
// 100 per page and 20 per client IP, each per hour.
func DefaultStatusSubscribersConfig() StatusSubscribersConfig {
	return StatusSubscribersConfig{
		ConfirmPerRecipientPerHour: DefaultStatusSubscriberConfirmPerRecipientPerHour,
		ConfirmPerPagePerHour:      DefaultStatusSubscriberConfirmPerPagePerHour,
		ConfirmPerIPPerHour:        DefaultStatusSubscriberConfirmPerIPPerHour,
	}
}

// applyStatusSubscribersEnv reads the SP_STATUS_SUBSCRIBERS_* budgets. A
// non-negative integer applies (0 disables the budget); anything else is
// ignored and the configured value stays.
func applyStatusSubscribersEnv(cfg *StatusSubscribersConfig) {
	read := func(raw string, dst *int) {
		if raw == "" {
			return
		}

		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			*dst = n
		}
	}

	read(os.Getenv("SP_STATUS_SUBSCRIBERS_CONFIRM_PER_RECIPIENT_PER_HOUR"), &cfg.ConfirmPerRecipientPerHour)
	read(os.Getenv("SP_STATUS_SUBSCRIBERS_CONFIRM_PER_PAGE_PER_HOUR"), &cfg.ConfirmPerPagePerHour)
	read(os.Getenv("SP_STATUS_SUBSCRIBERS_CONFIRM_PER_IP_PER_HOUR"), &cfg.ConfirmPerIPPerHour)
}
