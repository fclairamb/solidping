package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// fakeIncidents records what the interaction path asked the incident service
// to do, so the ack flow can be asserted without a real incidents service.
type fakeIncidents struct {
	ackOrgUID   string
	ackIncident string
	ackUserID   string
	ackUserName string
	ackErr      error

	commentIncident string
	commentText     string
	commentGuild    string
}

func (f *fakeIncidents) AcknowledgeIncidentFromDiscord(
	_ context.Context, orgUID, incidentUID, discordUserID, discordUsername string,
) (*models.Incident, error) {
	f.ackOrgUID = orgUID
	f.ackIncident = incidentUID
	f.ackUserID = discordUserID
	f.ackUserName = discordUsername

	if f.ackErr != nil {
		return nil, f.ackErr
	}

	return &models.Incident{UID: incidentUID, Number: 42}, nil
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
	f.commentIncident = incidentUID
	f.commentText = text
	f.commentGuild = guildID

	return &models.Event{}, nil
}

func (f *fakeIncidents) AddCommentFromDiscordCommand(
	_ context.Context, _, incidentUID, text, _, _, guildID string,
) (*models.Event, error) {
	f.commentIncident = incidentUID
	f.commentText = text
	f.commentGuild = guildID

	return &models.Event{}, nil
}

// installedService returns a service with the bot installed into a guild, plus
// the fake incident service behind it.
func installedService(t *testing.T) (context.Context, *Service, *fakeIncidents, string) {
	t.Helper()

	ctx, svc, fake := setupDiscordService(t)

	incidents := &fakeIncidents{}
	svc.incidentsService = incidents

	org := models.NewOrganization("acme-int", "acme")
	require.NoError(t, svc.db.CreateOrganization(ctx, org))

	_, err := svc.HandleOAuthCallback(ctx, "fake-code", installState(ctx, t, svc, org.Slug))
	require.NoError(t, err)

	require.NoError(t, svc.SetDefaultChannel(ctx, fake.guild.ID, "C-ALERTS", false))

	return ctx, svc, incidents, org.UID
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
	r.Empty(resp.Data.Components, "an acknowledged incident must not keep an Acknowledge button")
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

	r.Equal(incident.UID, incidents.commentIncident)
	r.Equal("restarting the pod", incidents.commentText)
	r.Equal("G-ACME", incidents.commentGuild)
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
	r.Empty(incidents.commentIncident)
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
	r.Equal(named.UID, incidents.commentIncident, "an explicit #N must never be silently substituted")
	r.Equal("actually about this one", incidents.commentText)
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
