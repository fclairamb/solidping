package telegramcb

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// countingDB observes how many incident lookups a command performs.
//
// It is what makes the "no incident lookup observable" requirement testable
// rather than aspirational: a qualified reference naming an org this chat is not
// linked to must be refused BEFORE any incident is read, otherwise the bot is an
// oracle for other organizations' incident numbers.
type countingDB struct {
	db.Service

	mu             sync.Mutex
	byNumberLookup int
	byUIDLookup    int
}

func (c *countingDB) GetIncidentByNumber(
	ctx context.Context, orgUID string, number int64,
) (*models.Incident, error) {
	c.mu.Lock()
	c.byNumberLookup++
	c.mu.Unlock()

	return c.Service.GetIncidentByNumber(ctx, orgUID, number)
}

func (c *countingDB) GetIncident(ctx context.Context, orgUID, uid string) (*models.Incident, error) {
	c.mu.Lock()
	c.byUIDLookup++
	c.mu.Unlock()

	return c.Service.GetIncident(ctx, orgUID, uid)
}

func (c *countingDB) lookups() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.byNumberLookup, c.byUIDLookup
}

// multiOrgEnv is a chat linked in TWO organizations, with the link ordering
// pinned: org A is the older link and therefore the one an unqualified
// contacts[0] pick would land on. Every "wrong org" assertion below depends on
// that, so the routing fix is what makes them pass, not luck.
type multiOrgEnv struct {
	*cmdEnv
	counting *countingDB
	orgB     *models.Organization
	userB    *models.User
}

func setupMultiOrgEnv(t *testing.T) *multiOrgEnv {
	t.Helper()

	ctx := context.Background()
	env := setupCommandEnv(t, false)

	orgB := models.NewOrganization("tg-org-b", "TG Org B")
	require.NoError(t, env.db.CreateOrganization(ctx, orgB))

	userB := models.NewUser("oncall-b@example.com")
	require.NoError(t, env.db.CreateUser(ctx, userB))

	now := time.Now()
	// Org A is linked an hour earlier, so it sorts first.
	linkChat(t, env.tgEnv, env.org.UID, env.user.UID, "@oncall-a", now.Add(-time.Hour))
	linkChat(t, env.tgEnv, orgB.UID, userB.UID, "@oncall-b", now)

	contacts := env.telegramContacts(t)
	require.Len(t, contacts, 2)
	require.Equal(t, env.org.UID, contacts[0].OrganizationUID, "org A must sort first")

	counting := &countingDB{Service: env.db}

	jobs := jobsvc.NewService(env.db.DB(), env.db, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(env.db, jobs, clock.Real{}, nil)
	env.handler = NewHandler(counting, env.handler.cfg, WithSender(env.sender), WithAcknowledger(svc))

	return &multiOrgEnv{cmdEnv: env, counting: counting, orgB: orgB, userB: userB}
}

// linkChat creates one verified telegram contact for the shared test chat.
func linkChat(t *testing.T, env *tgEnv, orgUID, userUID, label string, createdAt time.Time) {
	t.Helper()

	ctx := context.Background()

	contact := models.NewUserContact(
		userUID, orgUID, models.UserContactTypeTelegram, testChatID, label,
	)
	contact.CreatedAt = createdAt
	contact.VerifiedAt = &createdAt
	require.NoError(t, env.db.UpsertUserContact(ctx, contact))

	for _, stored := range env.telegramContacts(t) {
		if stored.OrganizationUID == orgUID {
			require.NoError(t, env.db.MarkUserContactVerified(ctx, stored.UID, createdAt))
		}
	}
}

// seedCheckIn creates one enabled check in an arbitrary org.
func (e *multiOrgEnv) seedCheckIn(t *testing.T, orgUID, slug string) *models.Check {
	t.Helper()

	check := models.NewCheck(orgUID, slug, "http")
	require.NoError(t, e.db.CreateCheck(context.Background(), check))

	return check
}

// seedIncidentIn opens an incident in an arbitrary org.
func (e *multiOrgEnv) seedIncidentIn(
	t *testing.T, orgUID string, check *models.Check, age time.Duration,
) *models.Incident {
	t.Helper()

	incident := models.NewIncident(orgUID, check.UID, time.Now().Add(-age), "down")
	require.NoError(t, e.db.CreateIncident(context.Background(), incident))

	return incident
}

func (e *multiOrgEnv) storedIncident(t *testing.T, orgUID, uid string) *models.Incident {
	t.Helper()

	incident, err := e.db.GetIncident(context.Background(), orgUID, uid)
	require.NoError(t, err)
	require.NotNil(t, incident)

	return incident
}

// --- Bare references in a multi-org chat ------------------------------------

// A bare "#1" in a chat linked to two orgs, where BOTH have a #1, must never be
// guessed at. This is the negative control of the whole spec: acking the wrong
// org's incident is silent — the page stops, and nobody notices the real outage
// is still unclaimed.
func TestMultiOrg_BareRefAmbiguousAcknowledgesNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupMultiOrgEnv(t)

	checkA := env.seedCheckIn(t, env.org.UID, "api")
	checkB := env.seedCheckIn(t, env.orgB.UID, "billing")
	incidentA := env.seedIncidentIn(t, env.org.UID, checkA, time.Minute)
	incidentB := env.seedIncidentIn(t, env.orgB.UID, checkB, 23*time.Minute)

	// Per-org sequential numbering means both really are #1.
	r.Equal(incidentA.Number, incidentB.Number)

	env.command(t, "/ack #1")

	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Contains(msgs[0], "more than one of your organizations")
	// Both candidates are offered, org-qualified so the answer is typeable.
	r.Contains(msgs[0], telegram.QualifiedIncidentRef(env.org.Slug, incidentA.Number))
	r.Contains(msgs[0], telegram.QualifiedIncidentRef(env.orgB.Slug, incidentB.Number))
	r.Contains(msgs[0], "api")
	r.Contains(msgs[0], "billing")

	// THE assertion: nothing was acknowledged, in either org.
	r.Nil(env.storedIncident(t, env.org.UID, incidentA.UID).AcknowledgedAt,
		"an ambiguous ref must acknowledge nothing in org A")
	r.Nil(env.storedIncident(t, env.orgB.UID, incidentB.UID).AcknowledgedAt,
		"an ambiguous ref must acknowledge nothing in org B")
}

// The same bare ref, when only ONE linked org has that number, is unambiguous
// and is acted on — the disambiguation must not become a wall the user has to
// climb over for every command.
func TestMultiOrg_BareRefWithASingleMatchIsActedOn(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupMultiOrgEnv(t)

	checkA := env.seedCheckIn(t, env.org.UID, "api")
	checkB := env.seedCheckIn(t, env.orgB.UID, "billing")
	env.seedIncidentIn(t, env.org.UID, checkA, time.Minute)
	first := env.seedIncidentIn(t, env.orgB.UID, checkB, time.Minute)
	second := env.seedIncidentIn(t, env.orgB.UID, checkB, time.Minute)

	// #2 exists only in org B — the org that does NOT sort first.
	r.EqualValues(2, second.Number)
	r.EqualValues(1, first.Number)

	env.command(t, "/ack #2")

	r.NotNil(env.storedIncident(t, env.orgB.UID, second.UID).AcknowledgedAt)
	// Credited to org B's own account, not to the first-sorting org's user.
	r.Equal(env.userB.UID, *env.storedIncident(t, env.orgB.UID, second.UID).AcknowledgedBy)
}

// A bare ref matching nothing anywhere keeps the plain not-found answer.
func TestMultiOrg_BareRefWithNoMatchIsNotFound(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupMultiOrgEnv(t)

	env.command(t, "/ack #99")

	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Contains(msgs[0], "No incident <b>#99</b>")
}

// --- Qualified references ----------------------------------------------------

// The routing fix in one test: org A sorts first and has its own #1, but
// "#tg-org-b:1" resolves in org B and acks org B's incident.
func TestMultiOrg_QualifiedRefResolvesInTheNamedOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupMultiOrgEnv(t)

	checkA := env.seedCheckIn(t, env.org.UID, "api")
	checkB := env.seedCheckIn(t, env.orgB.UID, "billing")
	incidentA := env.seedIncidentIn(t, env.org.UID, checkA, time.Minute)
	incidentB := env.seedIncidentIn(t, env.orgB.UID, checkB, time.Minute)
	r.Equal(incidentA.Number, incidentB.Number)

	env.command(t, "/ack "+telegram.QualifiedIncidentRef(env.orgB.Slug, incidentB.Number))

	r.NotNil(env.storedIncident(t, env.orgB.UID, incidentB.UID).AcknowledgedAt,
		"the named org's incident is the one that gets acked")
	r.Equal(env.userB.UID, *env.storedIncident(t, env.orgB.UID, incidentB.UID).AcknowledgedBy)
	r.Nil(env.storedIncident(t, env.org.UID, incidentA.UID).AcknowledgedAt,
		"the first-sorting org's same-numbered incident must be untouched")

	// The confirmation names the org, so the sender can see where it landed.
	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Contains(msgs[0], telegram.QualifiedIncidentRef(env.orgB.Slug, incidentB.Number))
	r.Contains(msgs[0], "billing")
}

// A slug the chat is not linked to answers "not connected" and — the part that
// matters — never looks an incident up, so the command cannot be used to probe
// another organization's incident numbers.
func TestMultiOrg_QualifiedRefToAnUnlinkedOrgLooksNothingUp(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupMultiOrgEnv(t)

	checkA := env.seedCheckIn(t, env.org.UID, "api")
	env.seedIncidentIn(t, env.org.UID, checkA, time.Minute)

	// A third org exists on the instance but this chat is not linked to it.
	other := models.NewOrganization("tg-org-c", "TG Org C")
	r.NoError(env.db.CreateOrganization(context.Background(), other))

	beforeByNumber, beforeByUID := env.counting.lookups()

	env.command(t, "/ack #tg-org-c:1")
	env.command(t, "/incident #tg-org-c:1")

	msgs := env.sender.messages()
	r.Len(msgs, 2)

	for _, msg := range msgs {
		r.Contains(msg, "isn't connected to")
		r.Contains(msg, "tg-org-c")
		// The answer must not distinguish a real org from an invented one.
		r.NotContains(strings.ToLower(msg), "no incident")
	}

	afterByNumber, afterByUID := env.counting.lookups()
	r.Equal(beforeByNumber, afterByNumber, "no incident may be looked up by number")
	r.Equal(beforeByUID, afterByUID, "no incident may be looked up by uid either")

	// And an org slug that exists nowhere gets the very same answer.
	env.command(t, "/ack #never-existed:1")
	r.Contains(env.sender.messages()[2], "isn't connected to")
}

// A qualified ref naming a LINKED org whose number does not exist answers
// not-found with the qualified reference, so the reader can tell which org was
// actually searched.
func TestMultiOrg_QualifiedRefNotFoundNamesTheOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupMultiOrgEnv(t)

	env.command(t, "/incident #tg-org-b:404")

	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Contains(msgs[0], "No incident <b>#tg-org-b:404</b>")
}

// --- Rendering ---------------------------------------------------------------

// Multi-org: every rendered reference is org-qualified, in the listing and in
// the detail view alike.
func TestMultiOrg_ListingsQualifyEveryRef(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupMultiOrgEnv(t)

	checkA := env.seedCheckIn(t, env.org.UID, "api")
	checkB := env.seedCheckIn(t, env.orgB.UID, "billing")
	incidentA := env.seedIncidentIn(t, env.org.UID, checkA, time.Minute)
	incidentB := env.seedIncidentIn(t, env.orgB.UID, checkB, time.Minute)

	env.command(t, "/incidents")

	msgs := env.sender.messages()
	// Two headers plus two rows.
	r.Len(msgs, 4)
	r.Contains(msgs[1], telegram.QualifiedIncidentRef(env.org.Slug, incidentA.Number))
	r.Contains(msgs[3], telegram.QualifiedIncidentRef(env.orgB.Slug, incidentB.Number))

	env.command(t, "/incident #tg-org-b:1")
	detail := env.sender.messages()[4]
	r.Contains(detail, telegram.QualifiedIncidentRef(env.orgB.Slug, incidentB.Number))
	r.Contains(detail, "billing")
}

// Single-org positive control: the overwhelming common case must notice
// NOTHING. Short refs everywhere — listing, detail and ack confirmation — and
// never a qualified one.
func TestSingleOrg_RefsStayShort(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)

	check := env.seedCheck(t, "api")
	incident := env.seedIncident(t, check, time.Minute)

	env.command(t, "/incidents")
	env.command(t, "/incident #1")
	env.command(t, "/ack #1")

	qualified := telegram.QualifiedIncidentRef(env.org.Slug, incident.Number)
	short := telegram.IncidentRef(incident.Number)

	msgs := env.sender.messages()
	r.Len(msgs, 4)

	for _, msg := range msgs[1:] {
		r.Contains(msg, short)
		r.NotContains(msg, qualified, "a single-org chat must never see a qualified ref")
	}
}

// --- Fan-out ------------------------------------------------------------------

// /status is read-only, so a multi-org chat gets one line per org rather than
// one line about whichever org sorted first.
func TestMultiOrg_StatusFansOutPerOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupMultiOrgEnv(t)

	env.seedCheckIn(t, env.org.UID, "api")
	checkB := env.seedCheckIn(t, env.orgB.UID, "billing")
	env.seedIncidentIn(t, env.orgB.UID, checkB, time.Minute)

	env.command(t, "/status")

	msgs := env.sender.messages()
	r.Len(msgs, 2)
	r.Contains(msgs[0], env.org.Slug)
	r.Contains(msgs[0], "all 1 checks up")
	r.Contains(msgs[1], env.orgB.Slug)
	r.Contains(msgs[1], "1 incident open")
}

// An org-slug argument scopes a read-only command to one linked org, and an
// unlinked slug gets the same not-connected answer as anywhere else.
func TestMultiOrg_StatusAndIncidentsScopeToOneOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupMultiOrgEnv(t)

	env.seedCheckIn(t, env.org.UID, "api")
	checkB := env.seedCheckIn(t, env.orgB.UID, "billing")
	env.seedIncidentIn(t, env.orgB.UID, checkB, time.Minute)

	env.command(t, "/status tg-org-b")
	env.command(t, "/incidents tg-org-b")
	env.command(t, "/status tg-org-c")

	msgs := env.sender.messages()
	// One status line, then one /incidents header + one row, then the refusal.
	r.Len(msgs, 4)
	r.Contains(msgs[0], env.orgB.Slug)
	r.NotContains(msgs[0], "all 1 checks up")
	r.Contains(msgs[1], env.orgB.Slug)
	r.Contains(msgs[2], "billing")
	r.Contains(msgs[3], "isn't connected to")
}

// A bare /ack aggregates across orgs: exactly one open incident anywhere is
// unambiguous even when it lives in the org that does NOT sort first.
func TestMultiOrg_BareAckAggregatesAcrossOrgs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupMultiOrgEnv(t)

	checkB := env.seedCheckIn(t, env.orgB.UID, "billing")
	incidentB := env.seedIncidentIn(t, env.orgB.UID, checkB, time.Minute)

	env.command(t, "/ack")

	stored := env.storedIncident(t, env.orgB.UID, incidentB.UID)
	r.NotNil(stored.AcknowledgedAt, "the only open incident anywhere needs no reference")
	r.Equal(env.userB.UID, *stored.AcknowledgedBy)
}

// With open incidents in both orgs, a bare /ack lists them — qualified — and
// acknowledges nothing.
func TestMultiOrg_BareAckWithSeveralOpenListsQualifiedCandidates(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupMultiOrgEnv(t)

	checkA := env.seedCheckIn(t, env.org.UID, "api")
	checkB := env.seedCheckIn(t, env.orgB.UID, "billing")
	incidentA := env.seedIncidentIn(t, env.org.UID, checkA, time.Minute)
	incidentB := env.seedIncidentIn(t, env.orgB.UID, checkB, time.Minute)

	env.command(t, "/ack")

	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Contains(msgs[0], "Which one?")
	r.Contains(msgs[0], telegram.QualifiedIncidentRef(env.org.Slug, incidentA.Number))
	r.Contains(msgs[0], telegram.QualifiedIncidentRef(env.orgB.Slug, incidentB.Number))

	r.Nil(env.storedIncident(t, env.org.UID, incidentA.UID).AcknowledgedAt)
	r.Nil(env.storedIncident(t, env.orgB.UID, incidentB.UID).AcknowledgedAt)
}

// Nothing open in either org keeps the plain answer.
func TestMultiOrg_BareAckWithNothingOpen(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupMultiOrgEnv(t)

	env.command(t, "/ack")

	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Contains(msgs[0], "Nothing to acknowledge")
}

// --- Inline Acknowledge button ------------------------------------------------

// The dead-end this spec exists to remove: a button attached to org B's alert,
// pressed in a chat where org A sorts first. It used to answer "that incident is
// no longer available" for an incident legitimately paged into this very chat.
func TestMultiOrg_CallbackAcksInTheOwningOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupMultiOrgEnv(t)

	checkB := env.seedCheckIn(t, env.orgB.UID, "billing")
	incidentB := env.seedIncidentIn(t, env.orgB.UID, checkB, time.Minute)

	env.pressAck(t, incidentB.UID, "Alice")

	toasts := env.sender.answeredToasts()
	r.Len(toasts, 1)
	r.Contains(toasts[0], "Acknowledged")
	r.NotContains(toasts[0], "no longer available")

	stored := env.storedIncident(t, env.orgB.UID, incidentB.UID)
	r.NotNil(stored.AcknowledgedAt)
	// The audit names org B's user row, not the first-sorting org's.
	r.Equal(env.userB.UID, *stored.AcknowledgedBy)
	r.NotEqual(env.user.UID, *stored.AcknowledgedBy)

	// The alert is repainted with a qualified ref, because this chat is
	// multi-org and "#1" would not say which org was taken.
	edits := env.sender.editedMessages()
	r.Len(edits, 1)
	r.Contains(edits[0].html, telegram.QualifiedIncidentRef(env.orgB.Slug, incidentB.Number))
}

// An incident UID belonging to no linked org keeps the unchanged answer — it
// still reveals nothing to a chat the incident was never paged to.
func TestMultiOrg_CallbackForAForeignIncidentIsRefused(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupMultiOrgEnv(t)

	// A real incident, in an org this chat is NOT linked to.
	other := models.NewOrganization("tg-org-c", "TG Org C")
	r.NoError(env.db.CreateOrganization(context.Background(), other))

	check := env.seedCheckIn(t, other.UID, "secret")
	foreign := env.seedIncidentIn(t, other.UID, check, time.Minute)

	env.pressAck(t, foreign.UID, "Mallory")

	toasts := env.sender.answeredToasts()
	r.Len(toasts, 1)
	r.Contains(toasts[0], "no longer available")
	r.Empty(env.sender.editedMessages())

	r.Nil(env.storedIncident(t, other.UID, foreign.UID).AcknowledgedAt,
		"a chat may never ack an incident from an org it is not linked to")
}
