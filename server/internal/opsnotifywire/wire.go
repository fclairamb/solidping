// Package opsnotifywire builds the opsnotify transport's media closures from
// the running process's service registry.
//
// It exists purely to break an import cycle. `internal/opsnotify` must stay a
// leaf because `internal/support` and `internal/handlers/auth` raise notices
// and both sit under `app/services` in the import graph. Everything that
// actually knows how to send — the job service, the Telegram client, the org's
// Slack connection, the web-push VAPID keys, the SMS resolver — lives above
// them, so it is assembled here and handed down as closures.
package opsnotifywire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/discord"
	slackclient "github.com/fclairamb/solidping/server/internal/integrations/slack"
	smssvc "github.com/fclairamb/solidping/server/internal/integrations/sms"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

// Wiring failures, all of them "this instance cannot carry that medium".
//
// Every one wraps opsnotify.ErrMediumUnavailable, which is what makes delivery
// count them as `skipped` rather than `failed`. That distinction is the whole
// point of the outcome label: an instance that never configured Telegram must
// not look, in solidping_operator_notice_total, like an instance whose Telegram
// is broken. Only a provider that was reachable and refused the message is a
// failure.
var (
	errNoJobService   = unavailable("job service unavailable")
	errNoTelegram     = unavailable("telegram is not configured on this instance")
	errNoSlackToken   = unavailable("no Slack access token for this organization")
	errNoWebPush      = unavailable("web push is not configured on this instance")
	errNoSMSResolver  = unavailable("no SMS resolver wired")
	errNoSMSProvider  = unavailable("no SMS provider available for this organization")
	errNoSlackChannel = unavailable("no Slack connection for this organization")
	errNoDiscordBot   = unavailable("the Discord bot is not configured on this instance")
	// errDiscordDMRefused is Discord code 50007 — the recipient does not accept
	// DMs from the bot. It sits with the "unavailable" errors rather than the
	// failures on purpose: the medium genuinely cannot carry this route, so
	// paging coverage must fall through to the member's NEXT route instead of
	// recording a delivery that never happened. A plain 5xx from Discord is a
	// failure and is left alone.
	errDiscordDMRefused = unavailable("discord refused the DM: the recipient does not accept them")
)

// unavailable builds a static "not configured" error carrying the sentinel.
func unavailable(reason string) error {
	return fmt.Errorf("%w: %s", opsnotify.ErrMediumUnavailable, reason)
}

// emailJobConfig is the wire shape of an email job's payload.
//
// Deliberately a local three-field struct rather than jobtypes.EmailJobConfig:
// `jobs/jobtypes` imports THIS package (to build the transport for the
// watchdog and the operator-notice job), so importing it back would be a
// cycle. The JSON tags are the contract and must match EmailJobConfig's.
type emailJobConfig struct {
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Text    string   `json:"text"`
}

// Build assembles the transport dependencies.
//
// Every argument is allowed to be nil: the matching closure is then left nil,
// which opsnotify reports as "this instance cannot deliver over that contact
// type" — a WARN and a `skipped` metric, never a silent drop. Closures capture
// the registry POINTER rather than its fields, so a service wired after Build
// (the boot order does exactly that) is still picked up at send time.
func Build(dbSvc db.Service, registry *services.Registry, cfg *config.Config) opsnotify.Deps {
	return opsnotify.Deps{
		DB:            dbSvc,
		EnqueueEmail:  enqueueEmail(registry),
		SendTelegram:  sendTelegram(cfg),
		SendSlackDM:   sendSlackDM(dbSvc, registry),
		SendDiscordDM: sendDiscordDM(dbSvc, cfg),
		SendWebPush:   sendWebPush(registry),
		SendSMS:       sendSMS(registry),
	}
}

// enqueueEmail hands the notice to the normal email job chain, so it inherits
// the retry policy and SMTP configuration every other mail uses.
func enqueueEmail(registry *services.Registry) opsnotify.EnqueueEmailFunc {
	return func(ctx context.Context, orgUID, to, subject, text string) error {
		if registry == nil || registry.Jobs == nil {
			return errNoJobService
		}

		payload, err := json.Marshal(emailJobConfig{To: []string{to}, Subject: subject, Text: text})
		if err != nil {
			return fmt.Errorf("encode operator notice email: %w", err)
		}

		if _, err := registry.Jobs.CreateJob(ctx, orgUID, string(jobdef.JobTypeEmail), payload, nil); err != nil {
			return fmt.Errorf("enqueue operator notice email: %w", err)
		}

		return nil
	}
}

// sendTelegram DMs through the instance bot. Configured(), not Active():
// reaching an already connected chat needs the token, never the bot @username.
func sendTelegram(cfg *config.Config) opsnotify.SendTelegramFunc {
	return func(ctx context.Context, chatID, html string) error {
		if cfg == nil || !cfg.Telegram.Configured() {
			return errNoTelegram
		}

		client, err := telegram.NewClientFromConfig(&cfg.Telegram)
		if err != nil {
			return fmt.Errorf("telegram client: %w", err)
		}

		if _, err := client.SendMessage(ctx, &telegram.Message{ChatID: chatID, HTML: html}); err != nil {
			return fmt.Errorf("send telegram: %w", err)
		}

		return nil
	}
}

// sendSlackDM delivers through the org's own Slack connection — the same path
// escalation DMs take.
func sendSlackDM(dbSvc db.Service, registry *services.Registry) opsnotify.SendSlackDMFunc {
	return func(ctx context.Context, orgUID, slackUserID, text string) error {
		if dbSvc == nil {
			return errNoSlackChannel
		}

		conn, err := dbSvc.GetSlackChannelForOrg(ctx, orgUID)
		if err != nil {
			return fmt.Errorf("%w: %w", errNoSlackChannel, err)
		}

		// The token lives in the encrypted settings_private envelope; the
		// registry pointer is read at send time so a credentials service wired
		// after Build is still picked up.
		var creds credentials.Service
		if registry != nil {
			creds = registry.Credentials
		}

		accessToken, tokenErr := slackclient.BotToken(ctx, creds, conn)
		if tokenErr != nil {
			if errors.Is(tokenErr, slackclient.ErrSlackNotConnected) {
				return errNoSlackToken
			}

			return fmt.Errorf("resolve slack bot token: %w", tokenErr)
		}

		client := slackclient.NewClient(accessToken)

		if _, err := client.PostMessage(ctx, slackclient.PostMessageOptions{
			Channel: slackUserID,
			Message: &slackclient.MessageResponse{Text: text},
		}); err != nil {
			return fmt.Errorf("post slack dm: %w", err)
		}

		return nil
	}
}

// sendDiscordDM delivers through the INSTANCE Discord bot, the way Telegram
// does — a Discord DM contact needs no org integration, only a configured bot.
//
// BotConfigured(), not just a token: the DM carries the incident action row, and
// its buttons reach us through the signature-verified interactions endpoint. An
// instance with a token but no public key would DM an Acknowledge button that
// does nothing when pressed.
func sendDiscordDM(dbSvc db.Service, cfg *config.Config) opsnotify.SendDiscordDMFunc {
	return func(ctx context.Context, contact *models.UserContact, text string) error {
		if cfg == nil || !cfg.Discord.BotConfigured() {
			return errNoDiscordBot
		}

		if contact == nil {
			return errNoDiscordBot
		}

		client := discord.NewBotClient(cfg.Discord.BotToken)

		_, err := discord.SendContactDM(ctx, client, dbSvc, contact, &discord.Message{Content: text})
		if err == nil {
			return nil
		}

		if discord.IsCannotDMUser(err) {
			return fmt.Errorf("%w: %w", errDiscordDMRefused, err)
		}

		return fmt.Errorf("send discord dm: %w", err)
	}
}

// sendWebPush pushes the headline to a stored browser subscription.
func sendWebPush(registry *services.Registry) opsnotify.SendWebPushFunc {
	return func(ctx context.Context, subscription, title, body, url string) error {
		if registry == nil || registry.WebPushOptions.VAPIDPublicKey == "" {
			return errNoWebPush
		}

		if err := webpush.Send(ctx, registry.WebPushOptions, subscription, webpush.Message{
			Title: title,
			Body:  body,
			URL:   url,
		}); err != nil {
			return fmt.Errorf("send web push: %w", err)
		}

		return nil
	}
}

// sendSMS texts through whichever sender the org resolves to — its own Twilio
// integration or the instance-level provider.
func sendSMS(registry *services.Registry) opsnotify.SendSMSFunc {
	return func(ctx context.Context, orgUID, number, body string) error {
		if registry == nil || registry.SMS == nil {
			return errNoSMSResolver
		}

		resolution, err := registry.SMS.Resolve(ctx, orgUID)
		if err != nil {
			return fmt.Errorf("%w: %w", errNoSMSProvider, err)
		}

		if resolution == nil || !resolution.SMSAvailable() {
			return errNoSMSProvider
		}

		if _, err := resolution.Sender.SendSMS(ctx, &smssvc.SendParams{To: number, Body: body}); err != nil {
			return fmt.Errorf("send sms: %w", err)
		}

		return nil
	}
}
