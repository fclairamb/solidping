package incidents_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

// TestAckActorPerOrigin is the headline of spec 2026-08-24-01 part A: whichever
// surface an acknowledgment came from, the incident API must be able to NAME
// the person who did it.
//
// One row per ack origin because the origins genuinely differ in kind, and the
// interesting half is the ones `incidents.acknowledged_by` cannot express: that
// column is a FK to `users.uid`, so a Slack, Discord or phone acker leaves it
// NULL and the event payload is the only surviving record. A test that only
// covered the dashboard path would pass while the feature stayed broken for
// every channel operators actually ack from.
func TestAckActorPerOrigin(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	type testCase struct {
		name string
		// ack performs the acknowledgment through the real entry point for
		// this origin, returning nothing — assertions read the API afterwards.
		ack func(t *testing.T, s *resolveSetup, userUID string)
		// wantName is the exact display name the API must resolve.
		wantName string
		// wantVia is the channel the API must report.
		wantVia string
		// wantUserUID asserts whether the platform account is credited too.
		wantUserUID bool
	}

	cases := []testCase{
		{
			name: "web — dashboard button, credited to the signed-in user",
			ack: func(t *testing.T, s *resolveSetup, userUID string) {
				t.Helper()
				_, err := s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
					IncidentUID:    s.incident.UID,
					AcknowledgedBy: userUID,
					Via:            "web",
				})
				require.NoError(t, err)
			},
			wantName:    "Alice Acme",
			wantVia:     "web",
			wantUserUID: true,
		},
		{
			name: "magic link — recipient is a known user",
			ack: func(t *testing.T, s *resolveSetup, userUID string) {
				t.Helper()
				_, err := s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
					IncidentUID:         s.incident.UID,
					AcknowledgedBy:      userUID,
					AcknowledgedByEmail: "alice@acme.com",
					Via:                 "email",
				})
				require.NoError(t, err)
			},
			wantName:    "Alice Acme",
			wantVia:     "email",
			wantUserUID: true,
		},
		{
			name: "magic link — recipient has no platform account",
			ack: func(t *testing.T, s *resolveSetup, _ string) {
				t.Helper()
				_, err := s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
					IncidentUID:         s.incident.UID,
					AcknowledgedByEmail: "oncall@acme.com",
					Via:                 "email",
				})
				require.NoError(t, err)
			},
			wantName: "oncall@acme.com",
			wantVia:  "email",
		},
		{
			name: "slack — no users row exists for the acker",
			ack: func(t *testing.T, s *resolveSetup, _ string) {
				t.Helper()
				_, err := s.svc.AcknowledgeIncidentFromSlack(
					ctx, s.org.UID, s.incident.UID, "U0ALICE", "alice", "T0ACME",
				)
				require.NoError(t, err)
			},
			wantName: "alice",
			wantVia:  "slack",
		},
		{
			name: "discord — no users row exists for the acker",
			ack: func(t *testing.T, s *resolveSetup, _ string) {
				t.Helper()
				_, err := s.svc.AcknowledgeIncidentFromDiscord(
					ctx, s.org.UID, s.incident.UID, "D0BOB", "bob", "G0ACME",
				)
				require.NoError(t, err)
			},
			wantName: "bob",
			wantVia:  "discord",
		},
		{
			name: "telegram — the person who pressed wins over the linked account",
			ack: func(t *testing.T, s *resolveSetup, userUID string) {
				t.Helper()
				// The chat is linked to Alice's account, but Carol pressed the
				// button in the shared group. Reporting "Alice" here would be
				// an outright wrong attribution, which is exactly why the
				// payload label exists.
				_, err := s.svc.AcknowledgeIncidentFromTelegram(
					ctx, s.org.UID, s.incident.UID, userUID, "via Telegram (Carol)",
				)
				require.NoError(t, err)
			},
			wantName:    "Carol",
			wantVia:     "telegram",
			wantUserUID: true,
		},
		{
			name: "telegram — an unnamed presser falls back to the linked account",
			ack: func(t *testing.T, s *resolveSetup, userUID string) {
				t.Helper()
				_, err := s.svc.AcknowledgeIncidentFromTelegram(
					ctx, s.org.UID, s.incident.UID, userUID, "via Telegram",
				)
				require.NoError(t, err)
			},
			wantName:    "Alice Acme",
			wantVia:     "telegram",
			wantUserUID: true,
		},
		{
			name: "phone — DTMF caller, identified only by their number",
			ack: func(t *testing.T, s *resolveSetup, _ string) {
				t.Helper()
				_, err := s.svc.AcknowledgeIncidentFromPhone(
					ctx, s.org.UID, s.incident.UID, "+33123456789",
				)
				require.NoError(t, err)
			},
			wantName: "+33123456789",
			wantVia:  "phone",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			s := newResolveSetup(t)
			user := newAckTestUser(t, s)

			tc.ack(t, s, user.UID)

			out, err := s.svc.GetIncident(ctx, s.org.Slug, s.incident.UID, nil)
			r.NoError(err)
			r.NotNil(out.AcknowledgedByActor, "an acknowledged incident must carry a resolved actor")
			r.Equal(tc.wantName, out.AcknowledgedByActor.Name)
			r.Equal(tc.wantVia, out.AcknowledgedByActor.Via)

			if tc.wantUserUID {
				r.Equal(user.UID, out.AcknowledgedByActor.UserUID)
			} else {
				r.Empty(out.AcknowledgedByActor.UserUID,
					"an acker with no platform account must not be credited to one")
			}
		})
	}
}

// An unacknowledged incident must carry NO actor at all — otherwise the
// dashboard renders "Acked by Unknown" on an incident nobody has taken, which
// is worse than showing nothing.
func TestUnacknowledgedIncidentHasNoActor(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	out, err := s.svc.GetIncident(ctx, s.org.Slug, s.incident.UID, nil)
	r.NoError(err)
	r.Nil(out.AcknowledgedByActor)
}

// Unacknowledging clears the attribution as well as the timestamp: an incident
// whose ack was withdrawn must not keep naming the person who withdrew it as
// its current acker.
func TestUnackClearsTheActor(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)
	user := newAckTestUser(t, s)

	_, err := s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
		IncidentUID:    s.incident.UID,
		AcknowledgedBy: user.UID,
		Via:            "web",
	})
	r.NoError(err)

	acked, err := s.svc.GetIncident(ctx, s.org.Slug, s.incident.UID, nil)
	r.NoError(err)
	r.NotNil(acked.AcknowledgedByActor)

	_, err = s.svc.UnacknowledgeIncident(ctx, s.org.Slug, s.incident.UID, user.UID, "web")
	r.NoError(err)

	out, err := s.svc.GetIncident(ctx, s.org.Slug, s.incident.UID, nil)
	r.NoError(err)
	r.Nil(out.AcknowledgedByActor, "a withdrawn acknowledgment leaves no actor")
	r.Nil(out.AcknowledgedAt)
	r.Nil(out.AcknowledgedBy)
}

// An ack → unack → re-ack cycle must report the CURRENT acker, not the first
// one: the resolver reads the latest acknowledgment event, and reading the
// oldest would freeze the attribution at whoever acked by mistake.
func TestReAckReportsTheCurrentActor(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)
	user := newAckTestUser(t, s)

	_, err := s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
		IncidentUID:    s.incident.UID,
		AcknowledgedBy: user.UID,
		Via:            "web",
	})
	r.NoError(err)

	_, err = s.svc.UnacknowledgeIncident(ctx, s.org.Slug, s.incident.UID, user.UID, "web")
	r.NoError(err)

	_, err = s.svc.AcknowledgeIncidentFromSlack(
		ctx, s.org.UID, s.incident.UID, "U0BOB", "bob", "T0ACME",
	)
	r.NoError(err)

	out, err := s.svc.GetIncident(ctx, s.org.Slug, s.incident.UID, nil)
	r.NoError(err)
	r.NotNil(out.AcknowledgedByActor)
	r.Equal("bob", out.AcknowledgedByActor.Name)
	r.Equal("slack", out.AcknowledgedByActor.Via)
}

// The ack endpoint's own response must already carry the attribution, so the
// dashboard does not flash "Acked" with no name until the query cache refetches.
func TestAckResponseCarriesTheActor(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)
	user := newAckTestUser(t, s)

	incident, err := s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
		IncidentUID:    s.incident.UID,
		AcknowledgedBy: user.UID,
		Via:            "web",
	})
	r.NoError(err)

	resp := s.svc.IncidentResponseWithAckActor(ctx, s.org.Slug, incident)
	r.NotNil(resp.AcknowledgedByActor)
	r.Equal("Alice Acme", resp.AcknowledgedByActor.Name)
	r.Equal("web", resp.AcknowledgedByActor.Via)
}

// newAckTestUser creates the org member the ack tests attribute acknowledgments
// to. Named, so the resolver's "name beats email" preference is observable.
func newAckTestUser(t *testing.T, s *resolveSetup) *models.User {
	t.Helper()

	user := models.NewUser("alice@acme.com")
	user.Name = "Alice Acme"
	require.NoError(t, s.dbSvc.CreateUser(t.Context(), user))

	return user
}
