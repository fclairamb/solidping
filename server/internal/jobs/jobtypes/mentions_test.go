package jobtypes

import (
	"context"
	"errors"
	"log/slog"
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

func newMentionFixture(t *testing.T, ctx context.Context, slug string, mentionOnCall bool) *mentionFixture {
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
func (f *mentionFixture) addUser(t *testing.T, ctx context.Context, email, name string) *models.User {
	t.Helper()

	user := models.NewUser(email)
	user.Name = name
	require.NoError(t, f.dbSvc.CreateUser(ctx, user))

	return user
}

// attachPolicy builds a one-step policy with the given targets and points the
// check at it.
func (f *mentionFixture) attachPolicy(
	t *testing.T, ctx context.Context, targets []*models.EscalationPolicyTarget,
) *models.EscalationPolicy {
	t.Helper()

	r := require.New(t)

	policy := models.NewEscalationPolicy(f.org.UID, "primary")
	r.NoError(f.dbSvc.CreateEscalationPolicy(ctx, policy))

	step := models.NewEscalationPolicyStep(policy.UID, 0, 0)
	r.NoError(f.dbSvc.ReplaceEscalationPolicySteps(ctx, policy.UID,
		[]*models.EscalationPolicyStep{step}, map[int][]*models.EscalationPolicyTarget{0: targets}))

	f.check.EscalationPolicyUID = &policy.UID
	r.NoError(f.dbSvc.UpdateCheck(ctx, f.check.UID, &models.CheckUpdate{EscalationPolicyUID: &policy.UID}))

	return policy
}

// mapIdentity records a Slack identity for a user on the fixture integration.
func (f *mentionFixture) mapIdentity(
	t *testing.T, ctx context.Context, user *models.User, externalID, displayName string,
) {
	t.Helper()

	require.NoError(t, f.dbSvc.UpsertUserIntegrationIdentity(ctx,
		models.NewUserIntegrationIdentity(
			f.org.UID, f.conn.UID, user.UID, externalID, displayName, models.IdentitySourceAuto)))
}

func userTarget(uid string, position int) *models.EscalationPolicyTarget {
	return models.NewEscalationPolicyTarget("", models.EscalationTargetUser, &uid, position)
}

func scheduleTarget(uid string, position int) *models.EscalationPolicyTarget {
	return models.NewEscalationPolicyTarget("", models.EscalationTargetSchedule, &uid, position)
}

// TestResolveOnCallMentionsUnionsAndOrders is the positive control: a step
// with both a schedule and a direct user target mentions BOTH, deduplicated
// and ordered by display name.
func TestResolveOnCallMentionsUnionsAndOrders(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMentionFixture(t, ctx, "mentions-union", true)

	zoe := fx.addUser(t, ctx, "zoe@acme.test", "Zoe")
	adam := fx.addUser(t, ctx, "adam@acme.test", "Adam")

	fx.mapIdentity(t, ctx, zoe, "U-ZOE", "Zoe")
	fx.mapIdentity(t, ctx, adam, "U-ADAM", "Adam")

	scheduleUID := "sched-union"
	registerOnCall(t, scheduleUID, zoe)

	fx.attachPolicy(t, ctx, []*models.EscalationPolicyTarget{
		scheduleTarget(scheduleUID, 0),
		userTarget(adam.UID, 1),
		// The same human twice (schedule + direct) must collapse to one mention.
		userTarget(zoe.UID, 2),
	})

	targets := ResolveOnCallMentions(
		ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created")

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
	fx := newMentionFixture(t, ctx, "mentions-off", false)

	adam := fx.addUser(t, ctx, "adam@acme.test", "Adam")
	fx.mapIdentity(t, ctx, adam, "U-ADAM", "Adam")
	fx.attachPolicy(t, ctx, []*models.EscalationPolicyTarget{userTarget(adam.UID, 0)})

	r.Nil(ResolveOnCallMentions(ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created"))
	r.Nil(ResolveOnCallMentions(ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.escalated"))
}

// TestResolveOnCallMentionsWithoutIdentityIsPlainText: a member with no
// mapping is named, never pinged.
func TestResolveOnCallMentionsWithoutIdentityIsPlainText(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMentionFixture(t, ctx, "mentions-noidentity", true)

	adam := fx.addUser(t, ctx, "adam@acme.test", "Adam")
	fx.attachPolicy(t, ctx, []*models.EscalationPolicyTarget{userTarget(adam.UID, 0)})

	targets := ResolveOnCallMentions(
		ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created")

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
	fx := newMentionFixture(t, ctx, "mentions-resolved", true)

	adam := fx.addUser(t, ctx, "adam@acme.test", "Adam")
	fx.mapIdentity(t, ctx, adam, "U-ADAM", "Adam")
	fx.attachPolicy(t, ctx, []*models.EscalationPolicyTarget{userTarget(adam.UID, 0)})

	// Positive control: the same fixture DOES mention on created.
	r.Len(ResolveOnCallMentions(
		ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created"), 1)

	for _, event := range []string{"incident.resolved", "incident.reopened", "incident.acked"} {
		r.Nil(ResolveOnCallMentions(ctx, fx.jctx, slog.Default(), fx.conn, fx.check, event),
			"event %s must never carry mentions", event)
	}
}

// TestResolveOnCallMentionsNoPolicyOrNoHumans: nothing to say, nothing said.
func TestResolveOnCallMentionsNoPolicyOrNoHumans(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMentionFixture(t, ctx, "mentions-nopolicy", true)

	// No policy at any level.
	r.Nil(ResolveOnCallMentions(ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created"))

	// A policy whose only step targets a connection — not a human.
	connUID := fx.conn.UID
	fx.attachPolicy(t, ctx, []*models.EscalationPolicyTarget{
		models.NewEscalationPolicyTarget("", models.EscalationTargetConnection, &connUID, 0),
		models.NewEscalationPolicyTarget("", models.EscalationTargetAllAdmins, nil, 1),
	})

	r.Nil(ResolveOnCallMentions(ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created"))
}

// TestResolveOnCallMentionsSurvivesScheduleFailure: an unresolvable schedule
// must degrade to "no mention for that target", never to a failed send.
func TestResolveOnCallMentionsSurvivesScheduleFailure(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMentionFixture(t, ctx, "mentions-schedfail", true)

	adam := fx.addUser(t, ctx, "adam@acme.test", "Adam")
	fx.mapIdentity(t, ctx, adam, "U-ADAM", "Adam")

	// Install the resolver without registering this schedule, so it errors.
	registerOnCall(t, "", nil)

	fx.attachPolicy(t, ctx, []*models.EscalationPolicyTarget{
		scheduleTarget("sched-never-registered", 0),
		userTarget(adam.UID, 1),
	})

	targets := ResolveOnCallMentions(
		ctx, fx.jctx, slog.Default(), fx.conn, fx.check, "incident.created")

	r.Len(targets, 1)
	r.Equal("U-ADAM", targets[0].ExternalID)
}

// TestResolveOnCallMentionsIgnoresNonSlackIntegrations: Teams mentions are out
// of scope, and a non-Slack integration has no mention_on_call to read.
func TestResolveOnCallMentionsIgnoresNonSlackIntegrations(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMentionFixture(t, ctx, "mentions-nonslack", true)

	adam := fx.addUser(t, ctx, "adam@acme.test", "Adam")
	fx.mapIdentity(t, ctx, adam, "U-ADAM", "Adam")
	fx.attachPolicy(t, ctx, []*models.EscalationPolicyTarget{userTarget(adam.UID, 0)})

	teams := models.NewIntegration(fx.org.UID, models.ConnectionTypeMSTeamsBot, "Teams")
	teams.Settings = models.JSONMap{"mention_on_call": true}
	r.NoError(fx.dbSvc.CreateChannel(ctx, teams))

	r.Nil(ResolveOnCallMentions(ctx, fx.jctx, slog.Default(), teams, fx.check, "incident.created"))
}

// TestResolveOnCallMentionsNilSafe: a missing job context or check must not
// panic the notification job.
func TestResolveOnCallMentionsNilSafe(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	fx := newMentionFixture(t, ctx, "mentions-nilsafe", true)

	r.Nil(ResolveOnCallMentions(ctx, nil, slog.Default(), fx.conn, fx.check, "incident.created"))
	r.Nil(ResolveOnCallMentions(ctx, fx.jctx, slog.Default(), fx.conn, nil, "incident.created"))
	r.Nil(ResolveOnCallMentions(ctx, fx.jctx, slog.Default(), nil, fx.check, "incident.created"))
}

// renderMentionsForTest renders targets the way the Slack sender does, so the
// "no ping without an identity" claim is asserted on real output.
func renderMentionsForTest(targets []notifications.MentionTarget) string {
	out := ""
	for _, target := range targets {
		if target.ExternalID != "" {
			out += "<@" + target.ExternalID + ">"

			continue
		}

		out += target.DisplayName
	}

	return out
}
