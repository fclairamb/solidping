package discord_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/integrations/discord"
)

func TestValidateWebhookURL(t *testing.T) {
	t.Parallel()

	valid := []string{
		"https://discord.com/api/webhooks/123456789/abc-DEF_123",
		"https://discordapp.com/api/webhooks/123456789/abc",
		"https://canary.discord.com/api/webhooks/1/tok",
		"https://ptb.discord.com/api/webhooks/1/tok",
		"https://canary.discordapp.com/api/webhooks/1/tok",
		"https://ptb.discordapp.com/api/webhooks/1/tok",
		"https://discord.com/api/v10/webhooks/1/tok",
		"https://DISCORD.com/api/webhooks/1/tok?thread_id=42",
		"  https://discord.com/api/webhooks/1/tok  ",
	}

	for _, raw := range valid {
		require.NoError(t, discord.ValidateWebhookURL(raw), raw)
	}

	invalid := []string{
		"",
		"not a url",
		"http://discord.com/api/webhooks/1/tok",
		"https://discord.example/hook",
		"https://evil.com/api/webhooks/1/tok",
		"https://discord.com.evil.com/api/webhooks/1/tok",
		"https://hooks.slack.com/services/T/B/x",
		"https://discord.com/channels/1/2",
		"https://discord.com/api/webhooks/1",
		"https://discord.com:8443/api/webhooks/1/tok",
		"https://user:pass@discord.com/api/webhooks/1/tok",
		"https://discord.gg/acme",
	}

	for _, raw := range invalid {
		require.ErrorIs(t, discord.ValidateWebhookURL(raw), discord.ErrInvalidWebhookURL, raw)
	}
}
