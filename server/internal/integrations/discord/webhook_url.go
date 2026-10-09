package discord

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// ErrInvalidWebhookURL is returned for a URL that is not a Discord channel
// webhook.
var ErrInvalidWebhookURL = errors.New(
	"webhook URL must be a Discord webhook (https://discord.com/api/webhooks/…)")

// webhookHosts are the hosts Discord hands out webhook URLs on: the main
// domain, the legacy discordapp.com one, and the canary/PTB client variants.
//
//nolint:gochecknoglobals // constant lookup table
var webhookHosts = map[string]bool{
	"discord.com":           true,
	"discordapp.com":        true,
	"canary.discord.com":    true,
	"ptb.discord.com":       true,
	"canary.discordapp.com": true,
	"ptb.discordapp.com":    true,
}

// webhookPath matches /api/webhooks/<id>/<token>, with an optional API
// version segment (/api/v10/webhooks/…), which Discord also accepts.
var webhookPath = regexp.MustCompile(`^/api/(v\d+/)?webhooks/\d+/[A-Za-z0-9_-]+/?$`)

// ValidateWebhookURL reports whether raw is a Discord channel webhook URL. It
// stops the field being used as a generic outbound webhook and catches a
// pasted channel link or invite at save time, instead of at the first alert.
func ValidateWebhookURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil ||
		!webhookHosts[strings.ToLower(parsed.Hostname())] || parsed.Port() != "" ||
		!webhookPath.MatchString(parsed.Path) {
		return ErrInvalidWebhookURL
	}

	return nil
}
