package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// fakeIncidents records what the interaction path asked the incident service
// to do, so the ack flow can be asserted without a real incidents service.
type fakeIncidents struct {
	ackOrgUID   string
	ackIncident string
	ackUserID   string
	ackUserName string
	ackGuildID  string
	ackErr      error
	// ackTitle overrides the acknowledged incident's title, so a test can put
	// operator-controlled text into the reply the ack update renders.
	ackTitle string

	// mu guards the recorded-comment fields below. Unlike the ack fields above
	// (only ever touched synchronously, from the same goroutine that dispatched
	// the interaction), a comment can be recorded by the Gateway supervisor's
	// background goroutine (gateway_test.go) while the test goroutine reads or
	// resets it — so those three need real synchronization, not just field
	// access.
	mu              sync.Mutex
	commentIncident string
	commentText     string
	commentGuild    string
}

func (f *fakeIncidents) AcknowledgeIncidentFromDiscord(
	_ context.Context, orgUID, incidentUID, discordUserID, discordUsername, guildID string,
) (*models.Incident, error) {
	f.ackOrgUID = orgUID
	f.ackIncident = incidentUID
	f.ackUserID = discordUserID
	f.ackUserName = discordUsername
	f.ackGuildID = guildID

	if f.ackErr != nil {
		return nil, f.ackErr
	}

	title := f.ackTitle
	if title == "" {
		title = "acme api is down"
	}

	return &models.Incident{UID: incidentUID, OrganizationUID: orgUID, Number: 42, Title: &title}, nil
}

func (f *fakeIncidents) GetIncidentByUID(
	_ context.Context, _, incidentUID string,
) (*models.Incident, error) {
	return &models.Incident{UID: incidentUID}, nil
}

func (f *fakeIncidents) GetCheckByUID(_ context.Context, _, checkUID string) (*models.Check, error) {
	return &models.Check{UID: checkUID}, nil
}

func (f *fakeIncidents) AddCommentFromDiscord(
	_ context.Context, _, incidentUID, text, _, _, guildID, _ string,
) (*models.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.commentIncident = incidentUID
	f.commentText = text
	f.commentGuild = guildID

	return &models.Event{}, nil
}

func (f *fakeIncidents) AddCommentFromDiscordCommand(
	_ context.Context, _, incidentUID, text, _, _, guildID string,
) (*models.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.commentIncident = incidentUID
	f.commentText = text
	f.commentGuild = guildID

	return &models.Event{}, nil
}

// CommentIncident returns the incident UID the most recently recorded comment
// was filed against. Locked because a Gateway test's background supervisor
// goroutine may still be writing it concurrently.
func (f *fakeIncidents) CommentIncident() string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.commentIncident
}

// CommentText returns the most recently recorded comment's text. See
// CommentIncident for why this goes through the lock.
func (f *fakeIncidents) CommentText() string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.commentText
}

// CommentGuild returns the guild ID the most recently recorded comment came
// from. See CommentIncident for why this goes through the lock.
func (f *fakeIncidents) CommentGuild() string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.commentGuild
}

// SetCommentText resets the recorded comment text, e.g. so a test can prove a
// second, redelivered message did not overwrite it. Locked for the same reason
// as the getters above.
func (f *fakeIncidents) SetCommentText(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.commentText = text
}

// installedService returns a service with the bot installed into a guild, plus
// the fake incident service behind it.
func installedService(t *testing.T) (context.Context, *Service, *fakeIncidents, string) {
	t.Helper()

	ctx, svc, incidents, orgUID, _ := installedServiceWithDiscord(t)

	return ctx, svc, incidents, orgUID
}

// installedServiceWithDiscord is installedService, plus the fake Discord the
// service's bot client points at, so a test can assert on the messages the
// flow posted (e.g. where the acknowledgment notice lands).
func installedServiceWithDiscord(t *testing.T) (
	context.Context, *Service, *fakeIncidents, string, *fakeDiscord,
) {
	t.Helper()

	ctx, svc, fake := setupDiscordService(t)

	incidents := &fakeIncidents{}
	svc.incidentsService = incidents
	// A real checks service over the same in-memory database, so command
	// replies that echo check names go through the production code path.
	svc.checksService = checks.NewService(svc.db, nil, nil, nil)

	org := models.NewOrganization("acme-int", "acme")
	require.NoError(t, svc.db.CreateOrganization(ctx, org))

	_, err := svc.HandleOAuthCallback(ctx, "fake-code", installState(ctx, t, svc, org.Slug))
	require.NoError(t, err)

	require.NoError(t, svc.SetDefaultChannel(ctx, fake.guild.ID, "C-ALERTS", false))

	return ctx, svc, incidents, org.UID, fake
}

// seedCheck creates a check so incidents can reference it, and returns its uid.
func seedCheck(ctx context.Context, t *testing.T, svc *Service, orgUID, slug string) string {
	t.Helper()

	check := models.NewCheck(orgUID, slug, "http")
	require.NoError(t, svc.db.CreateCheck(ctx, check))

	return check.UID
}

// TestHandleInteractions_PingAnswersPong: Discord's endpoint handshake. Getting
// this wrong means the interactions URL is never accepted at all.
func TestHandleInteractions_PingAnswersPong(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &config.Config{}
	h := NewHandler(NewService(nil, cfg, nil, nil), cfg)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/x",
		strings.NewReader(`{"type":1}`))

	r.NoError(h.HandleInteractions(rec, req))
	r.Equal(http.StatusOK, rec.Code)

	var resp InteractionResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Equal(InteractionCallbackPong, resp.Type)
}

// TestDispatchInteraction_AcknowledgeButton is the ack flow: the incident is
// acknowledged for the right org, and the message is UPDATED with its buttons
// removed so nobody acknowledges an already-handled incident.
func TestDispatchInteraction_AcknowledgeButton(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, incidents, orgUID := installedService(t)

	resp, err := DispatchInteraction(ctx, svc, &Interaction{
		Type:      InteractionTypeMessageComponent,
		GuildID:   "G-ACME",
		ChannelID: "C-ALERTS",
		Member:    &InteractionMember{User: &User{ID: "U-ALICE", Username: "alice"}, Nick: "alice"},
		Message:   &InteractionMessage{ID: "M-1", ChannelID: "C-ALERTS"},
		Data:      &InteractionData{CustomID: BuildCustomID(ActionAcknowledge, "inc-1"), ComponentType: 2},
	})
	r.NoError(err)

	r.Equal(orgUID, incidents.ackOrgUID)
	r.Equal("inc-1", incidents.ackIncident)
	r.Equal("U-ALICE", incidents.ackUserID)
	r.Equal("alice", incidents.ackUserName)

	r.Equal(InteractionCallbackUpdateMessage, resp.Type)
	r.NotNil(resp.Data)
	r.Contains(resp.Data.Content, "<@U-ALICE>")

	// Assert on the SERIALIZED response, not the Go struct.
	//
	// `r.Empty(resp.Data.Components)` would pass no matter what: the code just
	// assigned an empty slice, so the struct field is trivially empty. What
	// matters is whether `"components": []` reaches Discord — an UPDATE_MESSAGE
	// leaves every OMITTED field unchanged, so with `omitempty` on that field
	// the Acknowledge row would survive the acknowledgement and a second
	// responder could press it on an already-handled incident.
	encoded, err := json.Marshal(resp)
	r.NoError(err)

	var wire struct {
		Data struct {
			Components *[]map[string]any `json:"components"`
		} `json:"data"`
	}

	r.NoError(json.Unmarshal(encoded, &wire))
	r.NotNil(wire.Data.Components,
		"the acknowledgement must SEND an empty components array; an omitted key leaves Discord's buttons in place")
	r.Empty(*wire.Data.Components, "an acknowledged incident must not keep an Acknowledge button")

	// The embeds key, by contrast, must stay ABSENT: omitting it preserves the
	// incident card, while sending `[]` would erase it.
	r.NotContains(string(encoded), `"embeds"`)
}

// TestDispatchInteraction_AcknowledgeNoticePostsInTheThread pins where the
// acknowledgment notice lands: the incident's thread — the conversation the
// resolved and unacknowledged follow-ups join — not the channel the button
// lives in. The thread is resolved from the forward incident→thread state
// entry the notification sender wrote when it posted the alert.
func TestDispatchInteraction_AcknowledgeNoticePostsInTheThread(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _, orgUID, fake := installedServiceWithDiscord(t)

	// The forward entry the notification sender would have written when the
	// alert was posted: alert message in the alerts channel, thread opened off
	// it and recorded alongside.
	r.NoError(svc.db.SetStateEntry(ctx, &orgUID,
		IncidentThreadStateKey("inc-1"),
		&models.JSONMap{
			IncidentStateKeyChannelID: "C-ALERTS",
			IncidentStateKeyMessageID: "M-1",
			IncidentStateKeyThreadID:  "T-INC-1",
		}, nil))

	_, err := DispatchInteraction(ctx, svc, &Interaction{
		Type:      InteractionTypeMessageComponent,
		GuildID:   "G-ACME",
		ChannelID: "C-ALERTS",
		Member:    &InteractionMember{User: &User{ID: "U-ALICE", Username: "alice"}, Nick: "alice"},
		Message:   &InteractionMessage{ID: "M-1", ChannelID: "C-ALERTS"},
		Data:      &InteractionData{CustomID: BuildCustomID(ActionAcknowledge, "inc-1"), ComponentType: 2},
	})
	r.NoError(err)

	var threadNotices []postedMessage
	for _, msg := range fake.messages() {
		if content, _ := msg.Body["content"].(string); strings.Contains(content, "acknowledged the incident") {
			threadNotices = append(threadNotices, msg)
		}
	}

	r.Len(threadNotices, 1, "the acknowledgment notice must be posted exactly once")
	r.Equal("T-INC-1", threadNotices[0].ChannelID,
		"the acknowledgment notice must go into the incident's thread, not the alerts channel")
}

// TestDispatchInteraction_AcknowledgeNoticeFallsBackToTheChannel pins the
// no-thread fallback: with no recorded thread (thread creation denied at post
// time, or a DM destination) the notice still lands next to the alert rather
// than vanishing.
func TestDispatchInteraction_AcknowledgeNoticeFallsBackToTheChannel(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _, _, fake := installedServiceWithDiscord(t)

	_, err := DispatchInteraction(ctx, svc, &Interaction{
		Type:      InteractionTypeMessageComponent,
		GuildID:   "G-ACME",
		ChannelID: "C-ALERTS",
		Member:    &InteractionMember{User: &User{ID: "U-ALICE", Username: "alice"}, Nick: "alice"},
		Message:   &InteractionMessage{ID: "M-1", ChannelID: "C-ALERTS"},
		Data:      &InteractionData{CustomID: BuildCustomID(ActionAcknowledge, "inc-1"), ComponentType: 2},
	})
	r.NoError(err)

	var notices []string
	for _, msg := range fake.messages() {
		if content, _ := msg.Body["content"].(string); strings.Contains(content, "acknowledged the incident") {
			notices = append(notices, msg.ChannelID)
		}
	}

	r.Equal([]string{"C-ALERTS"}, notices)
}

// TestDispatchInteraction_AcknowledgeFromUnknownGuildIsRefused proves the
// authorization gate: a button press claiming a guild we know nothing about
// must not reach the incident service at all.
func TestDispatchInteraction_AcknowledgeFromUnknownGuildIsRefused(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, incidents, _ := installedService(t)

	resp, err := DispatchInteraction(ctx, svc, &Interaction{
		Type:    InteractionTypeMessageComponent,
		GuildID: "G-SOMEONE-ELSE",
		Member:  &InteractionMember{User: &User{ID: "U-MALLORY"}},
		Data:    &InteractionData{CustomID: BuildCustomID(ActionAcknowledge, "inc-1")},
	})
	r.NoError(err)

	r.Empty(incidents.ackIncident, "an unmapped guild must never acknowledge an incident")
	r.Equal(InteractionCallbackChannelMessage, resp.Type)
	r.Equal(MessageFlagEphemeral, resp.Data.Flags)
}

func TestDispatchInteraction_MalformedCustomIDIsRefused(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, incidents, _ := installedService(t)

	for _, customID := range []string{"", "ack", "ack:", ":inc-1", "nonsense"} {
		resp, err := DispatchInteraction(ctx, svc, &Interaction{
			Type:    InteractionTypeMessageComponent,
			GuildID: "G-ACME",
			Member:  &InteractionMember{User: &User{ID: "U-ALICE"}},
			Data:    &InteractionData{CustomID: customID},
		})
		r.NoError(err)
		r.Equal(InteractionCallbackChannelMessage, resp.Type)
		r.Empty(incidents.ackIncident, "custom_id %q must not acknowledge anything", customID)
	}
}

// --- application commands --------------------------------------------------

func TestCommandFromInteraction_FlattensSubcommands(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cmd := CommandFromInteraction(&Interaction{
		Type:      InteractionTypeApplicationCommand,
		GuildID:   "G-ACME",
		ChannelID: "C-ALERTS",
		Member:    &InteractionMember{User: &User{ID: "U-ALICE", Username: "alice"}},
		Data: &InteractionData{
			Name: "solidping",
			Options: []InteractionOption{{
				Name: "checks",
				Type: optionTypeSubCommandGroup,
				Options: []InteractionOption{{
					Name: "add",
					Type: optionTypeSubCommand,
					Options: []InteractionOption{
						{Name: "url", Type: 3, Value: "https://acme.com"},
						{Name: "slug", Type: 3, Value: "acme-home"},
					},
				}},
			}},
		},
	})

	r.Equal("checks", cmd.Command)
	r.Equal("add", cmd.Subcommand)
	r.Equal([]string{"https://acme.com", "acme-home"}, cmd.Args)
	r.Equal("G-ACME", cmd.GuildID)
	r.Equal("U-ALICE", cmd.UserID)
	r.Empty(cmd.ThreadID)
}

// TestCommandFromInteraction_DetectsThread: a command typed inside a thread
// reports the thread as its channel, which is how `comment` finds the incident
// without the user naming it.
func TestCommandFromInteraction_DetectsThread(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cmd := CommandFromInteraction(&Interaction{
		Type:      InteractionTypeApplicationCommand,
		GuildID:   "G-ACME",
		ChannelID: "T-THREAD",
		Channel:   &ChannelInfo{ID: "T-THREAD", Type: ChannelTypePublicThread},
		User:      &User{ID: "U-ALICE"},
		Data: &InteractionData{
			Name: "solidping",
			Options: []InteractionOption{{
				Name:    "comment",
				Type:    optionTypeSubCommand,
				Options: []InteractionOption{{Name: "text", Type: 3, Value: "restarting the pod"}},
			}},
		},
	})

	r.Equal("comment", cmd.Command)
	r.Equal("T-THREAD", cmd.ThreadID)
	r.Equal([]string{"restarting the pod"}, cmd.Args)
}

func TestDispatchCommand_HelpAndUnknown(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _, _ := installedService(t)

	help, err := DispatchCommand(ctx, svc, &Command{Command: cmdHelp, GuildID: "G-ACME"})
	r.NoError(err)
	r.Contains(help.Text, "/solidping checks list")
	r.False(help.Ephemeral)

	unknown, err := DispatchCommand(ctx, svc, &Command{Command: "definitely-not-a-command", GuildID: "G-ACME"})
	r.NoError(err)
	r.True(unknown.Ephemeral, "an unknown command must not be broadcast to the channel")
	r.Contains(unknown.Text, "Unknown command")
}

// TestDispatchCommand_UnconnectedGuildIsTold covers the guild that never
// installed the bot: an explanation, not a stack trace.
func TestDispatchCommand_UnconnectedGuildIsTold(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _, _ := installedService(t)

	resp, err := DispatchCommand(ctx, svc, &Command{Command: cmdChecks, Subcommand: subList, GuildID: "G-NOPE"})
	r.NoError(err)
	r.True(resp.Ephemeral)
	r.Contains(resp.Text, "not connected to SolidPing")
}

// TestDispatchCommand_CommentInThreadResolvesIncident exercises the thread
// resolution path end to end: no `#N` given, the thread mapping supplies the
// incident.
func TestDispatchCommand_CommentInThreadResolvesIncident(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, incidents, orgUID := installedService(t)

	// An incident, and the reverse mapping the sender writes when it opens the
	// incident's thread.
	incident := models.NewIncident(
		orgUID, seedCheck(ctx, t, svc, orgUID, "acme-api"), time.Now(), "acme api is down")
	incident.Number = 7
	r.NoError(svc.db.CreateIncident(ctx, incident))

	r.NoError(svc.db.SetStateEntry(ctx, nil,
		ReverseThreadStateKey("G-ACME", "T-THREAD"),
		&models.JSONMap{ThreadIncidentUIDKey: incident.UID, ThreadOrgUIDKey: orgUID}, nil))

	resp, err := DispatchCommand(ctx, svc, &Command{
		Command:  cmdComment,
		GuildID:  "G-ACME",
		ThreadID: "T-THREAD",
		UserID:   "U-ALICE",
		UserName: "alice",
		Args:     []string{"restarting", "the", "pod"},
	})
	r.NoError(err)
	r.True(resp.Ephemeral)

	r.Equal(incident.UID, incidents.CommentIncident())
	r.Equal("restarting the pod", incidents.CommentText())
	r.Equal("G-ACME", incidents.CommentGuild())
}

// TestDispatchCommand_CommentOutsideThreadNeedsAReference: commenting on the
// wrong incident is silent failure, so an unresolvable comment is refused
// rather than guessed at.
func TestDispatchCommand_CommentOutsideThreadNeedsAReference(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, incidents, _ := installedService(t)

	resp, err := DispatchCommand(ctx, svc, &Command{
		Command:   cmdComment,
		GuildID:   "G-ACME",
		ChannelID: "C-ALERTS",
		Args:      []string{"something", "broke"},
	})
	r.NoError(err)
	r.True(resp.Ephemeral)
	r.Contains(resp.Text, "not a SolidPing incident thread")
	r.Empty(incidents.CommentIncident())
}

// TestDispatchCommand_CommentByExplicitNumber pins rule 1: an explicit `#N`
// always wins over the thread the command was typed in.
func TestDispatchCommand_CommentByExplicitNumber(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, incidents, orgUID := installedService(t)

	named := models.NewIncident(
		orgUID, seedCheck(ctx, t, svc, orgUID, "named-check"), time.Now(), "named incident")
	named.Number = 11
	r.NoError(svc.db.CreateIncident(ctx, named))

	threaded := models.NewIncident(
		orgUID, seedCheck(ctx, t, svc, orgUID, "threaded-check"), time.Now(), "threaded incident")
	threaded.Number = 12
	r.NoError(svc.db.CreateIncident(ctx, threaded))

	r.NoError(svc.db.SetStateEntry(ctx, nil,
		ReverseThreadStateKey("G-ACME", "T-THREAD"),
		&models.JSONMap{ThreadIncidentUIDKey: threaded.UID, ThreadOrgUIDKey: orgUID}, nil))

	_, err := DispatchCommand(ctx, svc, &Command{
		Command:  cmdComment,
		GuildID:  "G-ACME",
		ThreadID: "T-THREAD",
		Args:     []string{"#11", "actually", "about", "this", "one"},
	})
	r.NoError(err)
	r.Equal(named.UID, incidents.CommentIncident(), "an explicit #N must never be silently substituted")
	r.Equal("actually about this one", incidents.CommentText())
}

func TestDispatchCommand_ConfigDefaultChannel(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _, _ := installedService(t)

	resp, err := DispatchCommand(ctx, svc, &Command{
		Command:    cmdConfig,
		Subcommand: "default-channel",
		GuildID:    "G-ACME",
		Args:       []string{"<#C-GENERAL>"},
	})
	r.NoError(err)
	r.Contains(resp.Text, "C-GENERAL")

	conn, err := svc.GetConnectionByGuildID(ctx, "G-ACME")
	r.NoError(err)

	settings, err := models.DiscordSettingsFromJSONMap(conn.Settings)
	r.NoError(err)
	r.Equal("C-GENERAL", settings.ChannelID)
}

func TestParseChannelReference(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("123", ParseChannelReference("<#123>"))
	r.Equal("123", ParseChannelReference("123"))
	r.Equal("C-1", ParseChannelReference("<#C-1>"))
	r.Empty(ParseChannelReference("#alerts"), "a plain #name cannot be resolved and must be refused")
	r.Empty(ParseChannelReference(""))
}

func TestParseMentionText(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cmd := ParseMentionText(`<@999> checks add https://acme.com -slug "acme home"`)
	r.Equal("checks", cmd.Command)
	r.Equal("add", cmd.Subcommand)
	r.Equal([]string{"https://acme.com"}, cmd.Args)
	r.Equal("acme home", cmd.Flags["slug"])

	// A nickname mention uses `<@!id>`.
	r.Equal("help", ParseMentionText(`<@!999>`).Command)
	r.Equal("help", ParseMentionText(`<@999>   `).Command)
}

func TestMentionsBot(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.True(MentionsBot("<@999> help", "999"))
	r.True(MentionsBot("  <@!999> help", "999"))
	r.False(MentionsBot("hello there", "999"))
	r.False(MentionsBot("<@111> help", "999"))
	r.False(MentionsBot("<@999> help", ""))
}

// TestDispatchApplicationCommand_ReplyCannotPingARole is the defense-in-depth
// half of the mention allow-list.
//
// A command reply interpolates text the caller controls — a check slug, a URL,
// an incident title. A check named `<@&123>` would otherwise turn a SolidPing
// reply into a role ping. `@everyone` is separately impossible because
// MENTION_EVERYONE is not in botPermissions, but role pings are not covered by
// that, and the alert path has carried an allow-list since it shipped.
//
// Asserted on the SERIALIZED response for the same reason as the components
// test above: what protects the guild is the JSON that reaches Discord.
func TestDispatchApplicationCommand_ReplyCannotPingARole(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _, orgUID := installedService(t)

	// A check whose identifier is a role mention. `checks list` echoes the
	// slug, so this is the string that actually reaches the reply.
	const roleMention = "<@&123456789012345678>"

	hostile := models.NewCheck(orgUID, roleMention, "http")
	r.NoError(svc.db.CreateCheck(ctx, hostile))

	resp, err := DispatchInteraction(ctx, svc, &Interaction{
		Type:      InteractionTypeApplicationCommand,
		GuildID:   "G-ACME",
		ChannelID: "C-ALERTS",
		Member:    &InteractionMember{User: &User{ID: "U-ALICE", Username: "alice"}},
		Data: &InteractionData{
			Name: "solidping",
			Options: []InteractionOption{{
				Name: "checks", Type: optionTypeSubCommandGroup,
				Options: []InteractionOption{{Name: "list", Type: optionTypeSubCommand}},
			}},
		},
	})
	r.NoError(err)

	// The dangerous text really is in the reply — otherwise this test would
	// pass for the wrong reason (nothing to neutralize).
	r.Contains(resp.Data.Content, roleMention)

	encoded, err := json.Marshal(resp)
	r.NoError(err)

	// Decoding the wire shape Discord receives; the snake_case keys are its
	// API's, not ours.
	//
	//nolint:tagliatelle // Discord API uses snake_case
	var wire struct {
		Data struct {
			AllowedMentions *struct {
				Parse []string `json:"parse"`
				Users []string `json:"users"`
			} `json:"allowed_mentions"`
		} `json:"data"`
	}

	r.NoError(json.Unmarshal(encoded, &wire))
	r.NotNil(wire.Data.AllowedMentions,
		"a command reply that echoes user-controlled text must carry an allow-list")
	r.Empty(wire.Data.AllowedMentions.Parse,
		"an empty parse list is what disables @everyone/@here/role pings")
	r.Empty(wire.Data.AllowedMentions.Users,
		"a checks listing mentions nobody, so no user id may be allowed through")
}

// TestAcknowledgeReply_AllowsOnlyTheAcker: the ack update legitimately mentions
// the person who pressed the button, and nobody else — an incident title is
// operator-controlled text and must not be able to ping a role.
func TestAcknowledgeReply_AllowsOnlyTheAcker(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, incidents, _ := installedService(t)

	// An incident title is free operator text, and it lands in the ack reply
	// through IncidentLabel.
	const roleMention = "<@&987654321098765432>"

	incidents.ackTitle = "outage " + roleMention

	resp, err := DispatchInteraction(ctx, svc, &Interaction{
		Type:    InteractionTypeMessageComponent,
		GuildID: "G-ACME",
		Member:  &InteractionMember{User: &User{ID: "U-ALICE", Username: "alice"}},
		Message: &InteractionMessage{ID: "M-1", ChannelID: "C-ALERTS"},
		Data:    &InteractionData{CustomID: BuildCustomID(ActionAcknowledge, "inc-1")},
	})
	r.NoError(err)

	r.Contains(resp.Data.Content, roleMention, "the reply really does carry the role mention")

	r.NotNil(resp.Data.AllowedMentions)
	r.Empty(resp.Data.AllowedMentions.Parse,
		"an empty parse list is what stops the title's role mention from pinging")
	r.Equal([]string{"U-ALICE"}, resp.Data.AllowedMentions.Users,
		"only the acker, who the content mentions by id, may be pinged")
}
