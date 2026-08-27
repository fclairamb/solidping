package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/integrations/discord"
	"github.com/fclairamb/solidping/server/internal/integrations/slack"
	smssvc "github.com/fclairamb/solidping/server/internal/integrations/sms"
	"github.com/fclairamb/solidping/server/internal/support"
)

// threadClock is a fixed creation time — none of these pre-flights look at it,
// and a fixed value keeps the fixtures readable.
var threadClock = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

// routeTestDB spins an in-memory database for the adapter pre-flights, which
// resolve LOCAL state and therefore need a real store rather than a mock.
func routeTestDB(t *testing.T) db.Service {
	t.Helper()

	ctx := t.Context()
	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	return dbSvc
}

func slackThread(teamID, channelID string) *models.SupportThread {
	thread := models.NewSupportThread(models.SupportChannelSlack, "U0ACME1234", threadClock)
	thread.ChannelContext = models.JSONMap{}

	if teamID != "" {
		thread.ChannelContext["teamId"] = teamID
	}

	if channelID != "" {
		thread.ChannelContext["channelId"] = channelID
	}

	return thread
}

// TestSlackReplyRoute_RequiresAStoredConnection is the field failure, in a test.
//
// A workspace whose app was installed from Slack's own dashboard (or whose
// connection was later deleted) keeps delivering message.im events — capture
// authenticates with the instance-level signing secret and needs no connection
// at all — while holding no bot token we could ever answer with. Before this
// pre-flight every such thread advertised canReply: true and swallowed the
// operator's reply as a stored `Delivery failed` row.
func TestSlackReplyRoute_RequiresAStoredConnection(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dbSvc := routeTestDB(t)

	slackSvc := slack.NewService(dbSvc, &config.Config{}, nil, nil, nil)
	route := slackReplyRoute(slackSvc)

	// No integrations row for this team at all — exactly the observed state.
	orphan := route(ctx, slackThread("T0ACME0002", "D0ACME2222"))
	r.False(orphan.CanReply)
	r.Contains(orphan.Reason, "no bot token")

	// A thread that lost its routing context is refused for a DIFFERENT and
	// correspondingly different-sounding reason.
	noContext := route(ctx, slackThread("", ""))
	r.False(noContext.CanReply)
	r.Contains(noContext.Reason, "no workspace or channel id")

	noChannel := route(ctx, slackThread("T0ACME0001", ""))
	r.False(noChannel.CanReply)
	r.Contains(noChannel.Reason, "no workspace or channel id")

	// POSITIVE CONTROL: install the workspace properly and the same thread
	// shape becomes answerable. Without this the assertions above would pass
	// just as happily against a route func that always says no.
	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	conn := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "Acme workspace")
	conn.Settings["team_id"] = "T0ACME0001"
	conn.Settings["access_token"] = "xoxb-test"
	r.NoError(dbSvc.CreateChannel(ctx, conn))

	connected := route(ctx, slackThread("T0ACME0001", "D0ACME1111"))
	r.True(connected.CanReply)
	r.Empty(connected.Reason)

	// And the orphaned workspace is STILL refused — per thread, not per
	// channel: one connected workspace must not make every Slack thread look
	// answerable, which is the entire bug.
	r.False(route(ctx, slackThread("T0ACME0002", "D0ACME2222")).CanReply)
}

// TestDiscordReplyRoute_RequiresAChannelIDAndABot covers both halves of a
// Discord route: the thread's DM channel id and the instance's bot token.
func TestDiscordReplyRoute_RequiresAChannelIDAndABot(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dbSvc := routeTestDB(t)

	withBot := &config.Config{}
	withBot.Discord.BotToken = "bot-token"

	newThread := func(channelID string) *models.SupportThread {
		thread := models.NewSupportThread(models.SupportChannelDiscord, "998877", threadClock)
		thread.ChannelContext = models.JSONMap{}

		if channelID != "" {
			thread.ChannelContext["channelId"] = channelID
		}

		return thread
	}

	configured := discordReplyRoute(discord.NewService(dbSvc, withBot, nil, nil))

	noChannel := configured(ctx, newThread(""))
	r.False(noChannel.CanReply)
	r.Contains(noChannel.Reason, "no channel id")

	// POSITIVE CONTROL.
	r.True(configured(ctx, newThread("1122334455")).CanReply)

	// Same thread, no bot token on the instance: refused, and for the reason an
	// operator would actually need to act on.
	unconfigured := discordReplyRoute(discord.NewService(dbSvc, &config.Config{}, nil, nil))

	noBot := unconfigured(ctx, newThread("1122334455"))
	r.False(noBot.CanReply)
	r.Contains(noBot.Reason, "bot is not configured")
}

// fakeSMSSender stands in for a configured instance SMS provider. The
// pre-flight must never call it — it only asks whether one exists.
type fakeSMSSender struct{ calls int }

func (f *fakeSMSSender) SendSMS(_ context.Context, _ *smssvc.SendParams) (*smssvc.SendResult, error) {
	f.calls++

	return &smssvc.SendResult{ProviderMessageID: "SM-fake"}, nil
}

// TestSMSReplyRoute_RequiresAResolvedSender covers the SMS pre-flight, plus the
// guard it must NOT touch.
func TestSMSReplyRoute_RequiresAResolvedSender(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dbSvc := routeTestDB(t)

	thread := models.NewSupportThread(models.SupportChannelSMS, "+33600000000", threadClock)

	// No SMS service wired at all.
	noService := &Server{services: &services.Registry{}}

	off := noService.smsReplyRoute(ctx, thread)
	r.False(off.CanReply)
	r.Contains(off.Reason, "not configured on this instance")

	// A resolver with no instance sender and no per-org integration: resolution
	// succeeds and reports "unavailable", which is a refusal, not an error.
	empty := &Server{services: &services.Registry{
		SMS: smssvc.NewResolver(dbSvc, nil, nil, nil, ""),
	}}

	unavailable := empty.smsReplyRoute(ctx, thread)
	r.False(unavailable.CanReply)
	r.Contains(unavailable.Reason, "no SMS sender is available")

	// POSITIVE CONTROL: an instance sender makes the same thread routable, and
	// the pre-flight does NOT send anything while deciding that.
	sender := &fakeSMSSender{}
	configured := &Server{services: &services.Registry{
		SMS: smssvc.NewResolver(dbSvc, nil, sender, nil, "+33700000000"),
	}}

	available := configured.smsReplyRoute(ctx, thread)
	r.True(available.CanReply)
	r.Empty(available.Reason)
	r.Zero(sender.calls, "a pre-flight must resolve routing, never send")
}

// TestRegisterSupportRepliers_RoutesTheChannelsThatNeedIt pins the wiring
// itself: the three channels whose reachability varies per thread must be
// registered WITH a pre-flight, and the two that are decided by instance config
// must not need one.
//
// Registering Slack with the plain RegisterReplier is precisely the bug this
// spec fixes, and nothing else in the suite would notice a regression to it.
func TestRegisterSupportRepliers_RoutesTheChannelsThatNeedIt(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	dbSvc := routeTestDB(t)

	cfg := &config.Config{}
	cfg.Discord.BotToken = "bot-token"

	server := &Server{config: cfg, services: &services.Registry{}}
	inbox := support.NewService(dbSvc, support.Options{})

	server.registerSupportRepliers(
		inbox,
		slack.NewService(dbSvc, cfg, nil, nil, nil),
		discord.NewService(dbSvc, cfg, nil, nil),
	)

	// Slack: registered, and answering per thread rather than per channel.
	orphan := route(t, inbox, slackThread("T0ACME0002", "D0ACME2222"))
	r.False(orphan.CanReply)
	r.Contains(orphan.Reason, "no bot token")

	// Discord: a real pre-flight too — an adapter exists, this thread has no
	// channel id.
	discordThread := models.NewSupportThread(models.SupportChannelDiscord, "998877", threadClock)

	noChannel := route(t, inbox, discordThread)
	r.False(noChannel.CanReply)
	r.Contains(noChannel.Reason, "no channel id")

	// Email is deliberately unregistered: replies go through the human mailbox.
	emailThread := models.NewSupportThread(models.SupportChannelEmail, "alice@acme.com", threadClock)

	noAdapter := route(t, inbox, emailThread)
	r.False(noAdapter.CanReply)
	r.Contains(noAdapter.Reason, "no reply adapter")
}

func route(t *testing.T, inbox *support.Service, thread *models.SupportThread) support.ReplyRoute {
	t.Helper()

	return inbox.ReplyRouteFor(t.Context(), thread)
}
