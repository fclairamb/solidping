package jobtypes

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/notifications"
)

// errScheduleUnknown is what the shared test resolver returns for a schedule
// nobody registered — the "on-call resolution failed" path.
var errScheduleUnknown = errors.New("no on-call registered for this schedule")

// The on-call resolver is a package-level seam (SetOnCallResolver). Tests must
// stay parallel-safe, so it is installed exactly once and dispatches on a
// concurrent map keyed by schedule UID: each test registers its own schedules
// and never observes another test's.
//
//nolint:gochecknoglobals // shared, race-free registry backing the one-time resolver install.
var (
	testOnCallRegistry sync.Map
	testOnCallOnce     sync.Once
)

// registerOnCall makes scheduleUID resolve to user for the rest of the run.
func registerOnCall(t *testing.T, scheduleUID string, user *models.User) {
	t.Helper()

	testOnCallOnce.Do(func() {
		SetOnCallResolver(func(
			_ context.Context, _ *jobdef.JobContext, uid string, _ time.Time,
		) (*models.User, error) {
			if v, ok := testOnCallRegistry.Load(uid); ok {
				return v.(*models.User), nil //nolint:forcetypeassert // only *models.User is ever stored
			}

			return nil, errScheduleUnknown
		})
	})

	if user != nil {
		testOnCallRegistry.Store(scheduleUID, user)
	}
}

// mentionFixture is an org with a Slack integration, a check and a policy.
type mentionFixture struct {
	dbSvc db.Service
	jctx  *jobdef.JobContext
	org   *models.Organization
	conn  *models.Integration
	check *models.Check
}

func newMentionFixture(ctx context.Context, t *testing.T, slug string, mentionOnCall bool) *mentionFixture {
	t.Helper()

	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization(slug, "Mention Test Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	settings := &models.SlackSettings{
		TeamID:        "T1",
		TeamName:      "Acme",
		AccessToken:   "xoxb-test",
		ChannelID:     "C1",
		MentionOnCall: mentionOnCall,
	}
	settingsMap, err := settings.ToJSONMap()
	r.NoError(err)

	conn := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "Acme Slack")
	conn.Settings = settingsMap
	r.NoError(dbSvc.CreateChannel(ctx, conn))

	check := models.NewCheck(org.UID, "api", "http")
	name := "API"
	check.Name = &name
	r.NoError(dbSvc.CreateCheck(ctx, check))

	return &mentionFixture{
		dbSvc: dbSvc,
		jctx:  &jobdef.JobContext{DBService: dbSvc, Logger: slog.Default()},
		org:   org,
		conn:  conn,
		check: check,
	}
}

// addUser creates a user (not necessarily a member — mention resolution keys
// off the policy, not membership).
func (f *mentionFixture) addUser(ctx context.Context, t *testing.T, email, name string) *models.User {
	t.Helper()

	user := models.NewUser(email)
	user.Name = name
	require.NoError(t, f.dbSvc.CreateUser(ctx, user))

	return user
}

// attachPolicy builds a one-step policy with the given targets and points the
// check at it.
func (f *mentionFixture) attachPolicy(
	ctx context.Context, t *testing.T, targets []*models.EscalationPolicyTarget,
) {
	t.Helper()

	r := require.New(t)

	policy := models.NewEscalationPolicy(f.org.UID, "primary")
	r.NoError(f.dbSvc.CreateEscalationPolicy(ctx, policy))

	step := models.NewEscalationPolicyStep(policy.UID, 0, 0)
	r.NoError(f.dbSvc.ReplaceEscalationPolicySteps(ctx, policy.UID,
		[]*models.EscalationPolicyStep{step}, map[int][]*models.EscalationPolicyTarget{0: targets}))

	f.check.EscalationPolicyUID = &policy.UID
	r.NoError(f.dbSvc.UpdateCheck(ctx, f.check.UID, &models.CheckUpdate{EscalationPolicyUID: &policy.UID}))
}

// mapIdentity records a Slack identity for a user on the fixture integration.
func (f *mentionFixture) mapIdentity(
	ctx context.Context, t *testing.T, user *models.User, externalID, displayName string,
) {
	t.Helper()

	require.NoError(t, f.dbSvc.UpsertUserIntegrationIdentity(ctx,
		models.NewUserIntegrationIdentity(
			f.org.UID, f.conn.UID, user.UID, externalID, displayName, models.IdentitySourceAuto)))
}

func userTarget(uid string, position int) *models.EscalationPolicyTarget {
	return models.NewEscalationPolicyTarget("", models.EscalationTargetUser, &uid, position)
}

// scheduleTarget builds a schedule target at position 0. Steps in these
// fixtures hold at most one schedule, so the position is not a parameter.
func scheduleTarget(uid string) *models.EscalationPolicyTarget {
	return models.NewEscalationPolicyTarget("", models.EscalationTargetSchedule, &uid, 0)
}

// TestResolveOnCallMentionsUnionsAndOrders is the positive control: a step
// with both a schedule and a direct user target mentions BOTH, deduplicated
// and ordered by display name.
func TestResolveOnCallMentionsUnionsAndOrders(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMentionFixture(ctx, t, "mentions-union", true)

	zoe := fx.addUser(ctx, t, "zoe@acme.test", "Zoe")
	adam := fx.addUser(ctx, t, "adam@acme.test", "Adam")

	fx.mapIdentity(ctx, t, zoe, "U-ZOE", "Zoe")
	fx.mapIdentity(ctx, t, adam, "U-ADAM", "Adam")

	scheduleUID := "sched-union"
	registerOnCall(t, scheduleUID, zoe)

	fx.attachPolicy(ctx, t, []*models.EscalationPolicyTarget{
		scheduleTarget(scheduleUID),
		userTarget(adam.UID, 1),
		// The same human twice (schedule + direct) must collapse to one mention.
		userTarget(zoe.UID, 2),
	})

	targets := ResolveOnCallMentions(
		ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created", "")

	r.Len(targets, 2)
	r.Equal("Adam", targets[0].DisplayName)
	r.Equal("U-ADAM", targets[0].ExternalID)
	r.Equal("Zoe", targets[1].DisplayName)
	r.Equal("U-ZOE", targets[1].ExternalID)
}

// TestResolveOnCallMentionsOffEmitsNothing is the primary negative test: with
// the toggle off, no mention is produced at all.
func TestResolveOnCallMentionsOffEmitsNothing(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMentionFixture(ctx, t, "mentions-off", false)

	adam := fx.addUser(ctx, t, "adam@acme.test", "Adam")
	fx.mapIdentity(ctx, t, adam, "U-ADAM", "Adam")
	fx.attachPolicy(ctx, t, []*models.EscalationPolicyTarget{userTarget(adam.UID, 0)})

	r.Nil(ResolveOnCallMentions(ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created", ""))
	r.Nil(ResolveOnCallMentions(ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.escalated", ""))
}

// TestResolveOnCallMentionsWithoutIdentityIsPlainText: a member with no
// mapping is named, never pinged.
func TestResolveOnCallMentionsWithoutIdentityIsPlainText(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMentionFixture(ctx, t, "mentions-noidentity", true)

	adam := fx.addUser(ctx, t, "adam@acme.test", "Adam")
	fx.attachPolicy(ctx, t, []*models.EscalationPolicyTarget{userTarget(adam.UID, 0)})

	targets := ResolveOnCallMentions(
		ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created", "")

	r.Len(targets, 1)
	r.Equal("Adam", targets[0].DisplayName)
	r.Empty(targets[0].ExternalID)
	r.NotContains(renderMentionsForTest(targets), "<@")
}

// TestResolveOnCallMentionsSkipsResolvedAndReopened: status updates never ping.
func TestResolveOnCallMentionsSkipsResolvedAndReopened(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMentionFixture(ctx, t, "mentions-resolved", true)

	adam := fx.addUser(ctx, t, "adam@acme.test", "Adam")
	fx.mapIdentity(ctx, t, adam, "U-ADAM", "Adam")
	fx.attachPolicy(ctx, t, []*models.EscalationPolicyTarget{userTarget(adam.UID, 0)})

	// Positive control: the same fixture DOES mention on created.
	r.Len(ResolveOnCallMentions(
		ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created", ""), 1)

	for _, event := range []string{"incident.resolved", "incident.reopened", "incident.acked"} {
		r.Nil(ResolveOnCallMentions(ctx, fx.jctx, slog.Default(), fx.conn, fx.check, event, ""),
			"event %s must never carry mentions", event)
	}
}

// TestResolveOnCallMentionsNoPolicyOrNoHumans: nothing to say, nothing said.
func TestResolveOnCallMentionsNoPolicyOrNoHumans(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMentionFixture(ctx, t, "mentions-nopolicy", true)

	// No policy at any level.
	r.Nil(ResolveOnCallMentions(ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created", ""))

	// A policy whose only step targets a connection — not a human.
	connUID := fx.conn.UID
	fx.attachPolicy(ctx, t, []*models.EscalationPolicyTarget{
		models.NewEscalationPolicyTarget("", models.EscalationTargetConnection, &connUID, 0),
		models.NewEscalationPolicyTarget("", models.EscalationTargetAllAdmins, nil, 1),
	})

	r.Nil(ResolveOnCallMentions(ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created", ""))
}

// TestResolveOnCallMentionsSurvivesScheduleFailure: an unresolvable schedule
// must degrade to "no mention for that target", never to a failed send.
func TestResolveOnCallMentionsSurvivesScheduleFailure(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMentionFixture(ctx, t, "mentions-schedfail", true)

	adam := fx.addUser(ctx, t, "adam@acme.test", "Adam")
	fx.mapIdentity(ctx, t, adam, "U-ADAM", "Adam")

	// Install the resolver without registering this schedule, so it errors.
	registerOnCall(t, "", nil)

	fx.attachPolicy(ctx, t, []*models.EscalationPolicyTarget{
		scheduleTarget("sched-never-registered"),
		userTarget(adam.UID, 1),
	})

	targets := ResolveOnCallMentions(
		ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created", "")

	r.Len(targets, 1)
	r.Equal("U-ADAM", targets[0].ExternalID)
}

// TestResolveOnCallMentionsIgnoresNonSlackIntegrations: Teams mentions are out
// of scope, and a non-Slack integration has no mention_on_call to read.
func TestResolveOnCallMentionsIgnoresNonSlackIntegrations(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMentionFixture(ctx, t, "mentions-nonslack", true)

	adam := fx.addUser(ctx, t, "adam@acme.test", "Adam")
	fx.mapIdentity(ctx, t, adam, "U-ADAM", "Adam")
	fx.attachPolicy(ctx, t, []*models.EscalationPolicyTarget{userTarget(adam.UID, 0)})

	teams := models.NewIntegration(fx.org.UID, models.ConnectionTypeMSTeamsBot, "Teams")
	teams.Settings = models.JSONMap{"mention_on_call": true}
	r.NoError(fx.dbSvc.CreateChannel(ctx, teams))

	r.Nil(ResolveOnCallMentions(ctx, fx.jctx, slog.Default(), teams, fx.check, "incident.created", ""))
}

// TestResolveOnCallMentionsNilSafe: a missing job context or check must not
// panic the notification job.
func TestResolveOnCallMentionsNilSafe(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMentionFixture(ctx, t, "mentions-nilsafe", true)

	r.Nil(ResolveOnCallMentions(ctx, nil, slog.Default(), fx.conn, fx.check, "incident.created", ""))
	r.Nil(ResolveOnCallMentions(ctx, fx.jctx, slog.Default(), fx.conn, nil, "incident.created", ""))
	r.Nil(ResolveOnCallMentions(ctx, fx.jctx, slog.Default(), nil, fx.check, "incident.created", ""))
}

// renderMentionsForTest renders targets the way the Slack sender does, so the
// "no ping without an identity" claim is asserted on real output.
func renderMentionsForTest(targets []notifications.MentionTarget) string {
	var out strings.Builder

	for i := range targets {
		if targets[i].ExternalID != "" {
			out.WriteString("<@" + targets[i].ExternalID + ">")

			continue
		}

		out.WriteString(targets[i].DisplayName)
	}

	return out.String()
}

// TestResolveOnCallMentionsSurvivesDatabaseFailure is the "never fail the send"
// guarantee, stated against a database that cannot answer at all: the resolver
// returns no mentions instead of erroring or panicking, and the caller sends
// exactly the message it would have sent before this feature existed.
//
// Structurally, ResolveOnCallMentions has no error return — there is no channel
// through which a resolution failure could reach the send path — and this test
// pins the runtime half of that claim.
func TestResolveOnCallMentionsSurvivesDatabaseFailure(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMentionFixture(ctx, t, "mentions-dbfail", true)

	adam := fx.addUser(ctx, t, "adam@acme.test", "Adam")
	fx.mapIdentity(ctx, t, adam, "U-ADAM", "Adam")
	fx.attachPolicy(ctx, t, []*models.EscalationPolicyTarget{userTarget(adam.UID, 0)})

	// Positive control first: with a working database this fixture DOES mention.
	r.Len(ResolveOnCallMentions(
		ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created", ""), 1)

	// Now break the database underneath it.
	r.NoError(fx.dbSvc.Close())

	r.NotPanics(func() {
		r.Nil(ResolveOnCallMentions(
			ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created", ""))
	})
}

// attachMultiStepPolicy builds a policy with one step per entry of `steps` and
// points the check at it. Returns the step UIDs in position order, which is
// what a caller needs to name "the step that fired".
func (f *mentionFixture) attachMultiStepPolicy(
	ctx context.Context, t *testing.T, steps [][]*models.EscalationPolicyTarget,
) []string {
	t.Helper()

	r := require.New(t)

	policy := models.NewEscalationPolicy(f.org.UID, "multi")
	r.NoError(f.dbSvc.CreateEscalationPolicy(ctx, policy))

	rows := make([]*models.EscalationPolicyStep, 0, len(steps))
	targets := make(map[int][]*models.EscalationPolicyTarget, len(steps))

	for i, stepTargets := range steps {
		rows = append(rows, models.NewEscalationPolicyStep(policy.UID, i, 60))
		targets[i] = stepTargets
	}

	r.NoError(f.dbSvc.ReplaceEscalationPolicySteps(ctx, policy.UID, rows, targets))

	f.check.EscalationPolicyUID = &policy.UID
	r.NoError(f.dbSvc.UpdateCheck(ctx, f.check.UID,
		&models.CheckUpdate{EscalationPolicyUID: &policy.UID}))

	uids := make([]string, 0, len(rows))
	for _, row := range rows {
		uids = append(uids, row.UID)
	}

	return uids
}

// TestResolveOnCallMentionsFollowsFiredStep is the heart of §1: an escalation
// message must name the humans the step that FIRED is paging, not step 1's.
//
// The negative control is the "step 1 wins" row: with a human already at step
// 1 and `stepUid` empty (an `incident.created`), the schedule sitting at step 2
// is NOT named. Without that row, a resolver that simply concatenated every
// step would pass every other case here.
func TestResolveOnCallMentionsFollowsFiredStep(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMentionFixture(ctx, t, "mentions-firedstep", true)

	adam := fx.addUser(ctx, t, "adam@acme.test", "Adam")
	zoe := fx.addUser(ctx, t, "zoe@acme.test", "Zoe")
	fx.mapIdentity(ctx, t, adam, "U-ADAM", "Adam")
	fx.mapIdentity(ctx, t, zoe, "U-ZOE", "Zoe")

	scheduleUID := "sched-firedstep"
	registerOnCall(t, scheduleUID, zoe)

	// step 1 → Adam (a direct user), step 2 → the on-call schedule (Zoe).
	steps := fx.attachMultiStepPolicy(ctx, t, [][]*models.EscalationPolicyTarget{
		{userTarget(adam.UID, 0)},
		{scheduleTarget(scheduleUID)},
	})

	tests := []struct {
		name      string
		eventType string
		stepUID   string
		want      []string
	}{
		{
			name:      "created resolves step 1 and NOT the schedule at step 2",
			eventType: "incident.created",
			stepUID:   "",
			want:      []string{"U-ADAM"},
		},
		{
			name:      "escalated on step 1 names step 1",
			eventType: "incident.escalated",
			stepUID:   steps[0],
			want:      []string{"U-ADAM"},
		},
		{
			name:      "escalated on step 2 names step 2, not step 1",
			eventType: "incident.escalated",
			stepUID:   steps[1],
			want:      []string{"U-ZOE"},
		},
		{
			name:      "a stale step uid degrades to step 1 rather than going silent",
			eventType: "incident.escalated",
			stepUID:   "step-from-a-deleted-policy",
			want:      []string{"U-ADAM"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			targets := ResolveOnCallMentions(
				ctx, fx.jctx, slog.Default(), fx.conn, fx.check, tc.eventType, tc.stepUID)

			ids := make([]string, 0, len(targets))
			for i := range targets {
				ids = append(ids, targets[i].ExternalID)
			}

			require.Equal(t, tc.want, ids)
		})
	}

	// And the whole point of the spec: no policy at all still says nothing.
	bare := newMentionFixture(ctx, t, "mentions-fired-nopol", true)
	r.Nil(ResolveOnCallMentions(
		ctx, bare.jctx, slog.Default(), bare.conn, bare.check, "incident.created", ""))
}

// TestResolveOnCallMentionsFallsBackToFirstHumanStep is the shape the spec was
// filed about: step 1 posts to a Slack connection (nobody human), step 2 pages
// the on-call schedule. An `incident.created` message must still name the
// person actually on call instead of saying nothing.
func TestResolveOnCallMentionsFallsBackToFirstHumanStep(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMentionFixture(ctx, t, "mentions-humanfb", true)

	zoe := fx.addUser(ctx, t, "zoe@acme.test", "Zoe")
	fx.mapIdentity(ctx, t, zoe, "U-ZOE", "Zoe")

	scheduleUID := "sched-humanfallback"
	registerOnCall(t, scheduleUID, zoe)

	connUID := fx.conn.UID
	steps := fx.attachMultiStepPolicy(ctx, t, [][]*models.EscalationPolicyTarget{
		{models.NewEscalationPolicyTarget("", models.EscalationTargetConnection, &connUID, 0)},
		{scheduleTarget(scheduleUID)},
	})

	created := ResolveOnCallMentions(
		ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created", "")
	r.Len(created, 1)
	r.Equal("U-ZOE", created[0].ExternalID)

	// Same fallback when the step that fired is itself the human-free one.
	escalated := ResolveOnCallMentions(
		ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.escalated", steps[0])
	r.Len(escalated, 1)
	r.Equal("U-ZOE", escalated[0].ExternalID)
}
