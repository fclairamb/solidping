package notifications

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

const discordTestDMChannel = "DM-CHANNEL-1"

// discordDMPayload is discordBotPayload with a DM destination: the same guild
// install, but ChannelID is a DM channel id and DMUserID names the member.
func discordDMPayload(t *testing.T, eventType string) *Payload {
	t.Helper()

	payload := discordBotPayload(t, eventType)

	settings := &models.DiscordSettings{
		GuildID:       discordTestGuild,
		GuildName:     "acme",
		ChannelID:     discordTestDMChannel,
		DMUserID:      "SNOW-ALICE",
		MentionOnCall: true,
	}

	settingsMap, err := settings.ToJSONMap()
	require.NoError(t, err)

	payload.Integration.Settings = settingsMap

	return payload
}

// TestDiscordSender_DMDestinationOpensNoThread is the explicit no-thread contract.
//
// A Discord DM channel is type 1 and cannot host a thread: Discord rejects
// POST /channels/{dm}/messages/{id}/threads. The sender must not even try —
// letting openThread swallow the error would work, but would log a misleading
// "could not open Discord incident thread" warning on EVERY alert and leave the
// contract untestable.
func TestDiscordSender_DMDestinationOpensNoThread(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newDiscordFake(t)
	db := &mockDBService{}

	r.NoError(fake.sender().Send(
		t.Context(), discordJCtx(db, discordTestBotToken), discordDMPayload(t, eventTypeIncidentCreated)))

	post := fake.find(http.MethodPost, func(p string) bool {
		return p == "/channels/"+discordTestDMChannel+"/messages"
	})
	r.NotNil(post, "the incident message must be posted into the DM channel")

	// The action row is still there: Acknowledge from a DM goes through the
	// existing interactions endpoint unchanged.
	components, ok := post.Body["components"].([]any)
	r.True(ok)
	r.Len(components, 1)

	// NOTHING may attempt a thread.
	for _, call := range fake.recorded() {
		r.False(strings.HasSuffix(call.Path, "/threads"),
			"a DM destination must never attempt to open a thread (got %s %s)", call.Method, call.Path)
	}
}

// TestDiscordSender_DMDestinationSkipsMentionOnCall is the resolved open question.
//
// A DM already has exactly one reader, so "<@them> — you are on call for this"
// pings the person already reading it, inside their own private conversation. It
// is noise, and it reads as a bug. mention_on_call stays true on the settings —
// the same integration may also post to a channel — it is simply not applied here.
func TestDiscordSender_DMDestinationSkipsMentionOnCall(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newDiscordFake(t)
	db := &mockDBService{}

	payload := discordDMPayload(t, eventTypeIncidentCreated)
	payload.OnCallMentions = []MentionTarget{
		{UserUID: "u1", DisplayName: "alice", ExternalID: "SNOW-ALICE"},
	}

	r.NoError(fake.sender().Send(t.Context(), discordJCtx(db, discordTestBotToken), payload))

	post := fake.find(http.MethodPost, func(p string) bool {
		return p == "/channels/"+discordTestDMChannel+"/messages"
	})
	r.NotNil(post)
	r.Empty(post.Body["content"], "a DM must carry no mention line")
	r.Nil(post.Body["allowed_mentions"])

	// POSITIVE CONTROL: the very same payload shape on a CHANNEL destination DOES
	// render the mention, so the assertion above is about the DM branch and not
	// about mentions being broken everywhere.
	channelFake := newDiscordFake(t)
	channelPayload := discordBotPayload(t, eventTypeIncidentCreated)
	channelPayload.OnCallMentions = payload.OnCallMentions

	r.NoError(channelFake.sender().Send(
		t.Context(), discordJCtx(&mockDBService{}, discordTestBotToken), channelPayload))

	channelPost := channelFake.find(http.MethodPost, func(p string) bool {
		return p == "/channels/"+discordTestChannel+"/messages"
	})
	r.NotNil(channelPost)
	r.Contains(channelPost.Body["content"], "<@SNOW-ALICE>")
}

// TestDiscordSender_DMFollowUpIsAPlainMessageReferencingTheOriginal: with no thread
// to reply into, a follow-up posts into the DM itself and carries a one-line
// pointer back to the original message — the thing a thread would have provided.
//
// Crucially it must also NOT call UnarchiveThread: there is no thread, and on a DM
// that PATCH would be an error on every single follow-up.
func TestDiscordSender_DMFollowUpIsAPlainMessageReferencingTheOriginal(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newDiscordFake(t)

	// The stored forward entry for a DM has an EMPTY thread id, which is exactly
	// what postIncidentMessage wrote for it.
	db := &mockDBService{
		getStateEntryFunc: func(_ context.Context, _ *string, key string) (*models.StateEntry, error) {
			if !strings.HasPrefix(key, "incidents/") {
				return nil, nil
			}

			return &models.StateEntry{Value: &models.JSONMap{
				discordKeyChannelID: discordTestDMChannel,
				discordKeyMessageID: discordTestMessage,
				discordKeyThreadID:  "",
			}}, nil
		},
	}

	r.NoError(fake.sender().Send(
		t.Context(), discordJCtx(db, discordTestBotToken), discordDMPayload(t, eventTypeIncidentResolved)))

	// The original embed was rewritten in place.
	edit := fake.find(http.MethodPatch, func(p string) bool {
		return p == "/channels/"+discordTestDMChannel+"/messages/"+discordTestMessage
	})
	r.NotNil(edit, "a resolve must still rewrite the original DM embed")

	// No un-archive: a DM has no thread to un-archive.
	for _, call := range fake.recorded() {
		if call.Method == http.MethodPatch && call.Path == "/channels/"+discordTestDMChannel {
			r.Fail("a DM destination must never attempt to un-archive a thread")
		}
	}

	// The follow-up landed in the DM, with the reference line.
	var followUp *discordCall

	for _, call := range fake.recorded() {
		if call.Method == http.MethodPost &&
			call.Path == "/channels/"+discordTestDMChannel+"/messages" {
			candidate := call
			followUp = &candidate
		}
	}

	r.NotNil(followUp, "the follow-up must post into the DM channel itself")

	content, ok := followUp.Body["content"].(string)
	r.True(ok, "a DM follow-up must carry a reference to the original")
	r.Contains(content, "/channels/@me/"+discordTestDMChannel+"/"+discordTestMessage,
		"the DM message link uses the @me form — a DM has no guild")
}

// TestDiscordSettingsIsDM pins the one predicate every branch above keys off.
func TestDiscordSettingsIsDM(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.True((&models.DiscordSettings{DMUserID: "SNOW"}).IsDM())
	r.False((&models.DiscordSettings{ChannelID: "C1", GuildID: "G1"}).IsDM())
	r.False((*models.DiscordSettings)(nil).IsDM())
}
