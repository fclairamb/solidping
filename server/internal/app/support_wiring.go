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
//
// REGISTRATION IS NOT REACHABILITY. Whether an adapter exists is an instance
// fact; whether it can reach one particular conversation is a per-thread fact,
// and the two diverge constantly — a Slack workspace whose app was installed
// from Slack's own dashboard sends us DMs we can capture and holds no bot token
// we could answer with. Every channel whose reachability varies per thread is
// therefore registered with RegisterRoutedReplier and a RouteFunc that resolves
// LOCAL state only (spec 2026-08-27-03). WhatsApp and Telegram stay on the plain
// form because their `if` above already is the whole answer.
func (s *Server) registerSupportRepliers(
	svc *support.Service, slackService *slack.Service, discordService *discord.Service,
) {
	if svc == nil {
		return
	}

	if s.config.WhatsApp.Active() {
		svc.RegisterReplier(models.SupportChannelWhatsApp, s.whatsAppReplier)
		svc.RegisterReadReceipt(models.SupportChannelWhatsApp, s.whatsAppReadReceipt)
	}

	if s.config.Telegram.Configured() {
		svc.RegisterReplier(models.SupportChannelTelegram, s.telegramReplier)
	}

	if s.config.SMS.Active() {
		svc.RegisterRoutedReplier(models.SupportChannelSMS, s.smsReplier, s.smsReplyRoute)
	}

	if slackService != nil {
		svc.RegisterRoutedReplier(
			models.SupportChannelSlack, slackReplier(slackService), slackReplyRoute(slackService))
	}

	if discordService != nil {
		svc.RegisterRoutedReplier(
			models.SupportChannelDiscord, discordReplier(discordService), discordReplyRoute(discordService))
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

// whatsAppReadReceipt marks externalID — and, cumulatively, every earlier
// message in the same WhatsApp conversation — read on Meta's Cloud API. This
// is the only thing that puts the double BLUE check on the sender's phone;
// see whatsapp.Client.MarkRead's doc comment for the cumulative-receipt
// caveat. thread is accepted only to satisfy support.ReadReceiptFunc — the
// Cloud API call needs nothing from it beyond the message id already
// resolved by the caller.
func (s *Server) whatsAppReadReceipt(
	ctx context.Context, _ *models.SupportThread, externalID string,
) error {
	client, err := whatsapp.NewClientFromConfig(&s.config.WhatsApp)
	if err != nil {
		return err
	}

	return client.MarkRead(ctx, externalID)
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

// slackReplyRoute is the pre-flight for a Slack thread.
//
// This is the failure the spec was written from: capture authenticates with the
// instance-level signing secret and needs no connection at all, while replying
// needs a stored bot token for that specific workspace. A workspace whose app
// was installed from Slack's dashboard rather than through SolidPing's OAuth
// callback — or whose connection was later deleted — opens support threads that
// can never be answered, and until now said "canReply: true" about every one of
// them.
//
// GetConnectionByTeamID is a local lookup (organization_providers, then
// integrations). No Slack API call happens here, deliberately: this runs once
// per thread on every inbox render.
func slackReplyRoute(svc *slack.Service) support.RouteFunc {
	return func(ctx context.Context, thread *models.SupportThread) support.ReplyRoute {
		teamID, _ := thread.ChannelContext["teamId"].(string)
		channelID, _ := thread.ChannelContext["channelId"].(string)

		if teamID == "" || channelID == "" {
			return support.ReplyRoute{
				Reason: "this Slack thread carries no workspace or channel id, so there is " +
					"nowhere to send a reply",
			}
		}

		if _, err := svc.GetConnectionByTeamID(ctx, teamID); err != nil {
			if errors.Is(err, slack.ErrConnectionNotFound) {
				return support.ReplyRoute{
					Reason: "SolidPing holds no bot token for this Slack workspace — the app " +
						"must be installed through SolidPing before replies can be sent",
				}
			}

			return support.ReplyRoute{
				Reason: "the Slack connection for this workspace could not be resolved",
			}
		}

		return support.ReplyRoute{CanReply: true}
	}
}

// discordReplyRoute is the pre-flight for a Discord thread: a DM channel id on
// the thread, and a bot token on the instance.
func discordReplyRoute(svc *discord.Service) support.RouteFunc {
	return func(ctx context.Context, thread *models.SupportThread) support.ReplyRoute {
		channelID, _ := thread.ChannelContext["channelId"].(string)
		if channelID == "" {
			return support.ReplyRoute{
				Reason: "this Discord thread carries no channel id, so there is nowhere to " +
					"send a reply",
			}
		}

		// GetClient only reads the configured bot token — no Discord API call.
		if _, err := svc.GetClient(ctx, ""); err != nil {
			return support.ReplyRoute{
				Reason: "the Discord bot is not configured on this instance",
			}
		}

		return support.ReplyRoute{CanReply: true}
	}
}

// smsReplyRoute is the pre-flight for an SMS thread: does this thread's org
// resolve to a sender at all?
//
// It runs the SAME resolution smsReplier runs and stops there. It deliberately
// does NOT touch the instance spend guards: a reservation consumed by rendering
// a list would bill an operator's inbox against the runaway ceiling that exists
// to cap actual sends.
func (s *Server) smsReplyRoute(ctx context.Context, thread *models.SupportThread) support.ReplyRoute {
	if s.services.SMS == nil {
		return support.ReplyRoute{Reason: "SMS is not configured on this instance"}
	}

	orgUID := ""
	if thread.OrganizationUID != nil {
		orgUID = *thread.OrganizationUID
	}

	resolution, err := s.services.SMS.Resolve(ctx, orgUID)
	if err != nil {
		return support.ReplyRoute{Reason: "an SMS route for this thread could not be resolved"}
	}

	if !resolution.SMSAvailable() {
		return support.ReplyRoute{
			Reason: "no SMS sender is available for this thread — neither this instance nor " +
				"the attributed organization has one configured",
		}
	}

	return support.ReplyRoute{CanReply: true}
}
