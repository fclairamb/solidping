package discord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// seedIncident creates a real incident row so the DM ack path's org narrowing
// (GetIncident per candidate org) exercises the database rather than a stub.
func seedIncident(ctx context.Context, t *testing.T, svc *Service, orgUID string) *models.Incident {
	t.Helper()

	r := require.New(t)

	checkUID := seedCheck(ctx, t, svc, orgUID, "api")

	incident := models.NewIncident(orgUID, checkUID, time.Now(), "api is down")
	r.NoError(svc.db.CreateIncident(ctx, incident))

	return incident
}

// seedDiscordContact binds a Discord user id to a member of orgUID.
func seedDiscordContact(
	ctx context.Context, t *testing.T, svc *Service, orgUID, email, snowflake string, verified bool,
) {
	t.Helper()

	r := require.New(t)

	user := models.NewUser(email)
	r.NoError(svc.db.CreateUser(ctx, user))
	r.NoError(svc.db.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(orgUID, user.UID, models.MemberRoleAdmin)))

	contact := models.NewUserContact(
		user.UID, orgUID, models.UserContactTypeDiscord, snowflake, "Discord")
	r.NoError(svc.db.UpsertUserContact(ctx, contact))

	if verified {
		r.NoError(svc.db.MarkUserContactVerified(ctx, contact.UID, time.Now()))
	}
}

// dmAckInteraction is an Acknowledge press from inside a DM: no guild id, and
// the invoker is the top-level User rather than a guild Member.
func dmAckInteraction(snowflake, incidentUID string) *Interaction {
	return &Interaction{
		Type:      InteractionTypeMessageComponent,
		GuildID:   "",
		ChannelID: "DM-1",
		User:      &User{ID: snowflake, Username: "alice"},
		Message:   &InteractionMessage{ID: "M-DM", ChannelID: "DM-1"},
		Data: &InteractionData{
			CustomID:      BuildCustomID(ActionAcknowledge, incidentUID),
			ComponentType: ComponentTypeButton,
		},
	}
}

// TestDispatchInteraction_AcknowledgeFromDMResolvesTheIncident is the assertion
// the whole "Acknowledge from a DM goes through the existing interactions
// endpoint unchanged" claim rests on.
//
// A DM interaction carries NO guild_id, and the acknowledge path resolved the org
// from the guild. So the button rendered perfectly in the DM and answered "this
// server is not connected" when pressed — inside a 1:1 conversation, which is
// baffling as well as wrong. The org now comes from the presser's own `discord`
// contact instead.
func TestDispatchInteraction_AcknowledgeFromDMResolvesTheIncident(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, incidents, orgUID := installedService(t)

	incident := seedIncident(ctx, t, svc, orgUID)
	seedDiscordContact(ctx, t, svc, orgUID, "alice@acme.test", "SNOW-ALICE", true)

	resp, err := DispatchInteraction(ctx, svc, dmAckInteraction("SNOW-ALICE", incident.UID))
	r.NoError(err)

	r.Equal(orgUID, incidents.ackOrgUID, "the DM ack must resolve the incident's own org")
	r.Equal(incident.UID, incidents.ackIncident)
	r.Equal("SNOW-ALICE", incidents.ackUserID)
	// No guild to echo-suppress: the channel alert SHOULD still be updated.
	r.Empty(incidents.ackGuildID)

	r.Equal(InteractionCallbackUpdateMessage, resp.Type)
	r.NotNil(resp.Data)
	r.Contains(resp.Data.Content, "<@SNOW-ALICE>")
}

// TestDispatchInteraction_AcknowledgeFromDMRefusesAStranger is the authorization
// negative, and it is the reason the resolution goes presser → org and never the
// other way round.
//
// An incident UID travels in every alert, every dashboard URL and every webhook
// payload. If a DM ack resolved the incident first and then trusted whoever
// pressed the button, holding a UID would be enough to acknowledge a stranger's
// incident — silencing their page — from a DM with our bot.
func TestDispatchInteraction_AcknowledgeFromDMRefusesAStranger(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, incidents, orgUID := installedService(t)

	incident := seedIncident(ctx, t, svc, orgUID)
	// Mallory has no contact in this org at all.

	resp, err := DispatchInteraction(ctx, svc, dmAckInteraction("SNOW-MALLORY", incident.UID))
	r.NoError(err)

	r.Empty(incidents.ackIncident,
		"a Discord account bound to nobody in this org must never acknowledge its incidents")
	r.Equal(InteractionCallbackChannelMessage, resp.Type)
	r.Equal(MessageFlagEphemeral, resp.Data.Flags)
}

// TestDispatchInteraction_AcknowledgeFromDMRefusesARevokedBinding: an unverified
// `discord` contact is a binding the member withdrew (or the instance cleared).
// It proves nothing about who that Discord account belongs to any more, so it
// must not authorize anything.
//
// The positive control is the first test above: the very same setup WITH a
// verified contact does acknowledge.
func TestDispatchInteraction_AcknowledgeFromDMRefusesARevokedBinding(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, incidents, orgUID := installedService(t)

	incident := seedIncident(ctx, t, svc, orgUID)
	seedDiscordContact(ctx, t, svc, orgUID, "alice@acme.test", "SNOW-ALICE", false)

	_, err := DispatchInteraction(ctx, svc, dmAckInteraction("SNOW-ALICE", incident.UID))
	r.NoError(err)
	r.Empty(incidents.ackIncident, "an unverified binding must not authorize an acknowledgment")
}

// TestDispatchInteraction_AcknowledgeFromDMCannotReachAnotherOrgsIncident: the
// presser is a bound member of org A, and the incident belongs to org B. Being
// bound SOMEWHERE is not being bound where it counts.
func TestDispatchInteraction_AcknowledgeFromDMCannotReachAnotherOrgsIncident(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, incidents, orgUID := installedService(t)

	// Alice is bound in the installed org…
	seedDiscordContact(ctx, t, svc, orgUID, "alice@acme.test", "SNOW-ALICE", true)

	// …and the incident lives in a different one.
	other := models.NewOrganization("acme-eu", "acme eu")
	r.NoError(svc.db.CreateOrganization(ctx, other))
	incident := seedIncident(ctx, t, svc, other.UID)

	_, err := DispatchInteraction(ctx, svc, dmAckInteraction("SNOW-ALICE", incident.UID))
	r.NoError(err)
	r.Empty(incidents.ackIncident,
		"a binding in another org must not reach this incident")
}

// TestDispatchInteraction_DMUnavailableAndEscalateStillWork: the other two buttons
// are log-only and need no org, so they must keep working from a DM rather than
// falling into the not-connected branch.
func TestDispatchInteraction_DMUnavailableAndEscalateStillWork(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _, _ := installedService(t)

	for _, action := range []string{ActionUnavailable, ActionEscalate} {
		resp, err := DispatchInteraction(ctx, svc, &Interaction{
			Type:      InteractionTypeMessageComponent,
			ChannelID: "DM-1",
			User:      &User{ID: "SNOW-ALICE", Username: "alice"},
			Data: &InteractionData{
				CustomID:      BuildCustomID(action, "inc-1"),
				ComponentType: ComponentTypeButton,
			},
		})
		r.NoError(err)
		r.Equal(InteractionCallbackChannelMessage, resp.Type)
		r.NotContains(resp.Data.Content, "not connected",
			"%s needs no org, so a DM press must not be told the server is unconnected", action)
	}
}
