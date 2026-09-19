package slack

import (
	"context"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Settings returns the effective Slack settings of an integration row,
// opening the private envelope the bot token lives in.
//
// Every bot-token reader in the codebase goes through this (the test button,
// escalation DMs, operator notices, the channel picker, inbound events), so
// there is exactly one answer to "where is the token" — spec 2026-09-18-02,
// where six readers each had their own wrong answer.
func Settings(
	ctx context.Context, creds credentials.Service, conn *models.Integration,
) (*models.SlackSettings, error) {
	merged, err := credentials.OpenConnectionSettings(ctx, creds, conn)
	if err != nil {
		return nil, err
	}

	settings, err := models.SlackSettingsFromJSONMap(models.JSONMap(merged))
	if err != nil {
		return nil, fmt.Errorf("parse slack settings: %w", err)
	}

	return settings, nil
}

// BotToken returns the bot access token of a Slack integration.
//
// ErrSlackNotConnected means the row genuinely carries no token (a
// manually-created stub, or an install that never completed) — distinct from
// credentials.ErrEncryptionDisabled, which means the token is there but this
// process cannot open it.
func BotToken(
	ctx context.Context, creds credentials.Service, conn *models.Integration,
) (string, error) {
	settings, err := Settings(ctx, creds, conn)
	if err != nil {
		return "", err
	}

	if settings.AccessToken == "" {
		return "", ErrSlackNotConnected
	}

	return settings.AccessToken, nil
}
