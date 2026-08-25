package app

import (
	"context"
	"errors"
	"log/slog"
	"strconv"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/discord"
	"github.com/fclairamb/solidping/server/internal/integrations/slack"
	smssvc "github.com/fclairamb/solidping/server/internal/integrations/sms"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
	"github.com/fclairamb/solidping/server/internal/integrations/twilio"
	"github.com/fclairamb/solidping/server/internal/integrations/whatsapp"
	"github.com/fclairamb/solidping/server/internal/support"
)

// errNoReplyRoute is returned when a thread carries no usable routing context.
var errNoReplyRoute = errors.New("thread has no reply routing context")

// registerSupportRepliers wires the outbound side of the support inbox.
//
// Adapters are REGISTERED here rather than imported by the support package,
// which is what keeps that package a leaf: the Slack and Discord integrations
// already depend on handlers, so importing them from support would close an
// import cycle. server.go imports everything anyway, so it is the natural place
// for the wiring.
//
// A channel with no adapter registered is not a crash — it reports canReply:
// false to the dashboard, which disables the reply box with a reason instead of
// letting the operator type into something that will fail at send time.
func (s *Server) registerSupportRepliers(
	svc *support.Service, slackService *slack.Service, discordService *discord.Service,
) {
	if svc == nil {
		return
	}

	if s.config.WhatsApp.Active() {
		svc.RegisterReplier(models.SupportChannelWhatsApp, s.whatsAppReplier)
	}

	if s.config.Telegram.Configured() {
		svc.RegisterReplier(models.SupportChannelTelegram, s.telegramReplier)
	}

	if s.config.SMS.Active() {
		svc.RegisterReplier(models.SupportChannelSMS, s.smsReplier)
	}

	if slackService != nil {
		svc.RegisterReplier(models.SupportChannelSlack, slackReplier(slackService))
	}

	if discordService != nil {
		svc.RegisterReplier(models.SupportChannelDiscord, discordReplier(discordService))
	}

	// Email is deliberately absent. In v1 email support is a human mailbox, not
	// a thread in the inbox — inbound email capture is a separate, later spec —
	// so there is nothing to reply *into* here. models.SupportThread.ReplyWindow
	// says so in operator-facing words rather than leaving a box that fails.
}

// whatsAppReplier sends a free-form WhatsApp text.
//
// The 24-hour window is checked by the service BEFORE this runs: outside it Meta
// refuses a free-form send outright and only an approved template may go, so
// discovering the lapse from a provider error would be strictly worse than
// refusing with an explanation.
func (s *Server) whatsAppReplier(
	ctx context.Context, thread *models.SupportThread, body string,
) (string, error) {
	client, err := whatsapp.NewClientFromConfig(&s.config.WhatsApp)
	if err != nil {
		return "", err
	}

	return client.SendText(ctx, thread.ChannelIdentity, body)
}

// telegramReplier sends a plain-text Telegram message.
//
// The body is HTML-escaped: Telegram is asked to parse HTML (that is how every
// alert is formatted), and an operator's reply containing a literal "<" would
// otherwise be either mangled or rejected outright.
func (s *Server) telegramReplier(
	ctx context.Context, thread *models.SupportThread, body string,
) (string, error) {
	client, err := telegram.NewClientFromConfig(&s.config.Telegram)
	if err != nil {
		return "", err
	}

	messageID, err := client.SendMessage(ctx, &telegram.Message{
		ChatID: thread.ChannelIdentity,
		HTML:   telegram.EscapeHTML(body),
	})
	if err != nil {
		return "", err
	}

	return strconv.FormatInt(messageID, 10), nil
}

// smsReplier sends an SMS reply.
//
// Replies pass the SAME instance spend guards as alerting (country allow-list
// and the global runaway-per-hour ceiling). Without that the support inbox would
// be an unmetered path to the SMS bill, reachable by anyone who can text the
// number and provoke an operator into answering.
func (s *Server) smsReplier(
	ctx context.Context, thread *models.SupportThread, body string,
) (string, error) {
	if s.services.SMS == nil {
		return "", errNoReplyRoute
	}

	orgUID := ""
	if thread.OrganizationUID != nil {
		orgUID = *thread.OrganizationUID
	}

	resolution, err := s.services.SMS.Resolve(ctx, orgUID)
	if err != nil {
		return "", err
	}

	if !resolution.SMSAvailable() {
		return "", errNoReplyRoute
	}

	// Guards apply to server-credential sends only, exactly as on the alerting
	// path: an org paying with its own Twilio account is not spending ours.
	if resolution.InstanceCredentialsForSMS() && s.services.Entitlements != nil {
		if guardErr := s.services.Entitlements.ReserveInstanceSMS(
			ctx, orgUID, thread.ChannelIdentity,
		); guardErr != nil {
			s.services.Entitlements.LogInstanceSMSBreach(ctx, slog.Default(), orgUID, guardErr)

			return "", guardErr
		}
	}

	result, err := resolution.Sender.SendSMS(ctx, &smssvc.SendParams{
		To: thread.ChannelIdentity,
		// The opt-out disclosure rides along for the same A2P 10DLC reason the
		// alerting path appends it.
		Body: body + twilio.OptOutFooter,
	})
	if err != nil {
		return "", err
	}

	return result.ProviderMessageID, nil
}

// slackReplier posts back into the IM channel the DM arrived on.
func slackReplier(svc *slack.Service) support.ReplyFunc {
	return func(ctx context.Context, thread *models.SupportThread, body string) (string, error) {
		teamID, _ := thread.ChannelContext["teamId"].(string)
		channelID, _ := thread.ChannelContext["channelId"].(string)

		if teamID == "" || channelID == "" {
			return "", errNoReplyRoute
		}

		client, err := svc.GetClient(ctx, teamID)
		if err != nil {
			return "", err
		}

		result, err := client.PostMessage(ctx, slack.PostMessageOptions{
			Channel: channelID,
			Message: &slack.MessageResponse{Text: body},
		})
		if err != nil {
			return "", err
		}

		return result.TS, nil
	}
}

// discordReplier posts back into the DM channel the message arrived on.
func discordReplier(svc *discord.Service) support.ReplyFunc {
	return func(ctx context.Context, thread *models.SupportThread, body string) (string, error) {
		channelID, _ := thread.ChannelContext["channelId"].(string)
		if channelID == "" {
			return "", errNoReplyRoute
		}

		client, err := svc.GetClient(ctx, "")
		if err != nil {
			return "", err
		}

		result, err := client.CreateMessage(ctx, channelID, &discord.Message{
			Content: body,
			// No pings from a support reply, ever: the operator is answering
			// one person, not summoning a channel.
			AllowedMentions: &discord.AllowedMentions{Parse: []string{}},
		})
		if err != nil {
			return "", err
		}

		return result.ID, nil
	}
}
