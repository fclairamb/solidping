package telegramcb

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
)

// member returns the linked user's membership in the env's org.
func (e *cmdEnv) member(t *testing.T) *models.OrganizationMember {
	t.Helper()

	member, err := e.db.GetMemberByUserAndOrg(context.Background(), e.user.UID, e.org.UID)
	require.NoError(t, err)

	return member
}

// setRole changes the linked user's role in the env's org.
func (e *cmdEnv) setRole(t *testing.T, role models.MemberRole) {
	t.Helper()

	require.NoError(t, e.db.UpdateOrganizationMember(context.Background(), e.member(t).UID,
		models.OrganizationMemberUpdate{Role: &role}))
}

// requireUntouched asserts an incident is still unacknowledged and uncommented.
func (e *cmdEnv) requireUntouched(t *testing.T, incident *models.Incident) {
	t.Helper()

	stored, err := e.db.GetIncident(context.Background(), e.org.UID, incident.UID)
	require.NoError(t, err)
	require.Nil(t, stored.AcknowledgedAt, "the incident must stay unacknowledged")
	require.Empty(t, e.incidentComments(t, incident.UID), "the incident must get no comment")
}

// TestRemovedMember_ChatActsAsUnlinked: a chat linked in an org whose
// membership is then removed answers exactly as an unlinked chat, for reads
// and writes, and changes nothing. Two removal paths: the membership row alone
// (the guard, as if a cleanup were missed) and the real removal, which also
// ends the contact.
func TestRemovedMember_ChatActsAsUnlinked(t *testing.T) {
	t.Parallel()

	removals := map[string]func(t *testing.T, env *cmdEnv){
		"membership row only (guard)": func(t *testing.T, env *cmdEnv) {
			t.Helper()

			_, err := env.db.DB().NewUpdate().
				Model((*models.OrganizationMember)(nil)).
				Set("deleted_at = ?", time.Now()).
				Where("uid = ?", env.member(t).UID).
				Exec(context.Background())
			require.NoError(t, err)
			require.Len(t, env.telegramContacts(t), 1, "the contact is deliberately left live")
		},
		"member removal (cleanup)": func(t *testing.T, env *cmdEnv) {
			t.Helper()

			removed, err := env.db.RemoveOrganizationMember(context.Background(), env.member(t).UID)
			require.NoError(t, err)
			require.Equal(t, 1, removed)
		},
	}

	for name, remove := range removals {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := setupCommandEnv(t, true)
			check := env.seedCheck(t, "api")

			// Positive control: while a member, the chat acks and comments.
			before := env.seedIncident(t, check, time.Minute)
			env.command(t, "/ack "+telegram.IncidentRef(before.Number))
			env.command(t, "/comment "+telegram.IncidentRef(before.Number)+" looking")

			stored, err := env.db.GetIncident(context.Background(), env.org.UID, before.UID)
			r.NoError(err)
			r.NotNil(stored.AcknowledgedAt)
			r.Len(env.incidentComments(t, before.UID), 1)

			after := env.seedIncident(t, check, time.Minute)
			ref := telegram.IncidentRef(after.Number)

			remove(t, env)

			sent := len(env.sender.messages())
			commands := []string{
				"/status", "/incidents", "/incident " + ref, "/ack " + ref, "/comment " + ref + " x",
			}

			for _, text := range commands {
				env.command(t, text)
			}

			msgs := env.sender.messages()[sent:]
			r.Len(msgs, len(commands))

			for i, msg := range msgs {
				r.Equal(telegram.BuildNotLinkedHTML(), msg, commands[i])
			}

			env.pressAck(t, after.UID, "Alice")

			toasts := env.sender.answeredToasts()
			r.Equal("This chat is not connected to SolidPing.", toasts[len(toasts)-1])
			r.Empty(env.sender.editedMessages())

			env.requireUntouched(t, after)
		})
	}
}

// TestViewer_ReadsButCannotWrite: a viewer's linked chat keeps the read
// commands, and is refused /ack, the ack button and /comment.
func TestViewer_ReadsButCannotWrite(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)
	env.setRole(t, models.MemberRoleViewer)

	check := env.seedCheck(t, "api")
	incident := env.seedIncident(t, check, time.Minute)
	ref := telegram.IncidentRef(incident.Number)

	// Reads answer normally (positive control), without an Ack button.
	env.command(t, "/status")
	env.command(t, "/incidents")

	msgs := env.sender.messages()
	r.Len(msgs, 3, "status line, incidents header, one incident row")
	r.Contains(msgs[1], "1 incident")
	r.Contains(msgs[2], ref)

	for _, msg := range msgs {
		r.NotEqual(telegram.BuildNotLinkedHTML(), msg)
	}

	for _, keyboard := range env.sender.sentKeyboards() {
		if keyboard == nil {
			continue
		}

		for _, row := range keyboard.InlineKeyboard {
			for _, button := range row {
				r.NotEqual(telegram.AckCallbackData(incident.UID), button.CallbackData,
					"a viewer is not offered an Acknowledge button")
			}
		}
	}

	// Writes are refused.
	sent := len(msgs)

	env.command(t, "/ack "+ref)
	env.command(t, "/ack")
	env.command(t, "/comment "+ref+" on it")
	env.command(t, "/comment on it")

	refused := env.sender.messages()[sent:]
	r.Len(refused, 4)

	for _, msg := range refused {
		r.Equal(telegram.BuildReadOnlyRoleHTML(env.org.Slug), msg)
	}

	env.pressAck(t, incident.UID, "Alice")

	toasts := env.sender.answeredToasts()
	r.Len(toasts, 1)
	r.Equal(telegram.ReadOnlyRoleToast(env.org.Slug), toasts[0])
	r.Empty(env.sender.editedMessages())

	env.requireUntouched(t, incident)

	// Positive control: promoted to user, the same chat acks and comments.
	env.setRole(t, models.MemberRoleUser)
	env.command(t, "/comment "+ref+" on it")
	env.pressAck(t, incident.UID, "Alice")

	stored, err := env.db.GetIncident(context.Background(), env.org.UID, incident.UID)
	r.NoError(err)
	r.NotNil(stored.AcknowledgedAt)
	r.Len(env.incidentComments(t, incident.UID), 1)
}

// TestLinkedOrgs_SameOrgPrefersTheWriter: two accounts of the same org linked
// to one chat act as the more privileged one, so a viewer's older link does not
// make a user's chat read-only.
func TestLinkedOrgs_SameOrgPrefersTheWriter(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	env := setupCommandEnv(t, false)

	viewer := models.NewUser("viewer@acme.com")
	r.NoError(env.db.CreateUser(ctx, viewer))
	r.NoError(env.db.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(env.org.UID, viewer.UID, models.MemberRoleViewer)))

	now := time.Now()
	linkChat(t, env.tgEnv, env.org.UID, viewer.UID, "@viewer", now.Add(-time.Hour))

	orgs := env.handler.linkedOrgs(ctx, testChatID)
	r.Len(orgs, 1)
	r.Equal(models.MemberRoleViewer, orgs[0].role)

	contact := models.NewUserContact(env.user.UID, env.org.UID, models.UserContactTypeTelegram, testChatID, "@user")
	contact.VerifiedAt = &now
	r.NoError(env.db.UpsertUserContact(ctx, contact))

	orgs = env.handler.linkedOrgs(ctx, testChatID)
	r.Len(orgs, 1)
	r.Equal(models.MemberRoleUser, orgs[0].role)
	r.Equal(env.user.UID, orgs[0].contact.UserUID)
}
