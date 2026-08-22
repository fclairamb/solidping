package events_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/events"
)

// newTestDB spins an in-memory, fully migrated SQLite service. A real database
// rather than a fake store: the visibility gate is half Go and half SQL (the
// NOT LIKE exclusion lives in the store), and a fake would only test the half
// that is not the security boundary.
func newTestDB(t *testing.T) db.Service {
	t.Helper()

	ctx := t.Context()

	svc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, svc.Initialize(ctx))

	t.Cleanup(func() { _ = svc.Close() })

	return svc
}

type fixture struct {
	db      db.Service
	svc     *events.Service
	org     *models.Organization
	admin   *models.User
	viewer  *models.User
	orgSlug string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	ctx := t.Context()
	dbSvc := newTestDB(t)

	org := models.NewOrganization("acme", "Acme")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	admin := models.NewUser("admin@acme.com")
	admin.Name = "Admin Person"
	require.NoError(t, dbSvc.CreateUser(ctx, admin))

	viewer := models.NewUser("viewer@acme.com")
	viewer.Name = "Viewer Person"
	require.NoError(t, dbSvc.CreateUser(ctx, viewer))

	require.NoError(t, dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, admin.UID, models.MemberRoleAdmin)))
	require.NoError(t, dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, viewer.UID, models.MemberRoleViewer)))

	return &fixture{
		db:      dbSvc,
		svc:     events.NewService(dbSvc),
		org:     org,
		admin:   admin,
		viewer:  viewer,
		orgSlug: org.Slug,
	}
}

// seed writes one event, at an explicit time so ordering is deterministic.
func (f *fixture) seed(
	ctx context.Context, t *testing.T,
	eventType models.EventType, actorUID *string, at time.Time, sourceIP string,
) *models.Event {
	t.Helper()

	event := models.NewEvent(f.org.UID, eventType, models.ActorTypeUser)
	event.ActorUID = actorUID
	event.CreatedAt = at

	if sourceIP != "" {
		event.SourceIP = &sourceIP
	}

	require.NoError(t, f.db.CreateEvent(ctx, event))

	return event
}

// seedWithPayload writes one event carrying an explicit payload, so the
// target-filter tests exercise the same payload shape internal/audit writes.
func (f *fixture) seedWithPayload(
	ctx context.Context, t *testing.T,
	eventType models.EventType, at time.Time, payload models.JSONMap,
) {
	t.Helper()

	event := models.NewEvent(f.org.UID, eventType, models.ActorTypeUser)
	event.CreatedAt = at
	event.Payload = payload

	require.NoError(t, f.db.CreateEvent(ctx, event))
}

func typesOf(resp *events.ListEventsResponse) []string {
	out := make([]string, 0, len(resp.Data))
	for _, item := range resp.Data {
		out = append(out, item.EventType)
	}

	return out
}

// TestAuthEventsAreInvisibleToNonAdmins is the security guard the spec asks
// for. Three assertions, and all three are load-bearing:
//
//  1. an admin sees the auth event (positive control — without it a ListEvents
//     that returned nothing at all would "pass");
//  2. a viewer's unfiltered listing does not contain it;
//  3. a viewer who asks for it BY NAME still does not get it. That is the
//     assertion a UI-only gate would fail.
func TestAuthEventsAreInvisibleToNonAdmins(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	f := newFixture(t)

	base := time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC)
	f.seed(ctx, t, models.EventTypeAuthLoginSucceeded, &f.admin.UID, base, "203.0.113.7")
	f.seed(ctx, t, models.EventTypeAuthLoginFailed, nil, base.Add(time.Minute), "203.0.113.8")
	f.seed(ctx, t, models.EventTypeCheckCreated, &f.admin.UID, base.Add(2*time.Minute), "203.0.113.9")

	adminView, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
		Size:   50,
		Caller: events.Caller{UserUID: f.admin.UID},
	})
	r.NoError(err)
	r.Contains(typesOf(adminView), "auth.login_succeeded")
	r.Contains(typesOf(adminView), "auth.login_failed")
	r.Contains(typesOf(adminView), "check.created")

	viewerView, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
		Size:   50,
		Caller: events.Caller{UserUID: f.viewer.UID},
	})
	r.NoError(err)
	r.NotContains(typesOf(viewerView), "auth.login_succeeded")
	r.NotContains(typesOf(viewerView), "auth.login_failed")
	// Positive control: the viewer is not simply seeing an empty list.
	r.Contains(typesOf(viewerView), "check.created")

	// Asking for the restricted family by name does not unlock it.
	viewerAsking, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
		Size:              50,
		EventTypePrefixes: []string{"auth"},
		Caller:            events.Caller{UserUID: f.viewer.UID},
	})
	r.NoError(err)
	r.Empty(viewerAsking.Data, "?type=auth must not be a way around the admin gate")

	// Nor does naming the exact type.
	viewerExact, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
		Size:       50,
		EventTypes: []string{"auth.login_succeeded"},
		Caller:     events.Caller{UserUID: f.viewer.UID},
	})
	r.NoError(err)
	r.Empty(viewerExact.Data, "?eventType=auth.login_succeeded must not be a way around the admin gate")
}

// TestSourceIPIsAdminOnly — the address a colleague works from is the same
// class of information as the auth family itself.
func TestSourceIPIsAdminOnly(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	f := newFixture(t)

	f.seed(ctx, t, models.EventTypeCheckCreated, &f.admin.UID,
		time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC), "203.0.113.9")

	adminView, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
		Size:   10,
		Caller: events.Caller{UserUID: f.admin.UID},
	})
	r.NoError(err)
	r.Len(adminView.Data, 1)
	r.NotNil(adminView.Data[0].SourceIP, "positive control: an admin does see the address")
	r.Equal("203.0.113.9", *adminView.Data[0].SourceIP)

	viewerView, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
		Size:   10,
		Caller: events.Caller{UserUID: f.viewer.UID},
	})
	r.NoError(err)
	r.Len(viewerView.Data, 1, "the event itself is not restricted, only its provenance")
	r.Nil(viewerView.Data[0].SourceIP)
}

// TestSuperAdminSeesRestrictedFamilies — a super admin is not a member of the
// org at all, so the membership lookup fails; the bypass must be explicit.
func TestSuperAdminSeesRestrictedFamilies(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	f := newFixture(t)

	f.seed(ctx, t, models.EventTypeAuthLoginSucceeded, nil,
		time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC), "")

	resp, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
		Size:   10,
		Caller: events.Caller{UserUID: "not-a-member", SuperAdmin: true},
	})
	r.NoError(err)
	r.Contains(typesOf(resp), "auth.login_succeeded")

	// Positive control: the same non-member WITHOUT the flag sees nothing
	// restricted, so the assertion above is testing the flag, not the fixture.
	stranger, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
		Size:   10,
		Caller: events.Caller{UserUID: "not-a-member"},
	})
	r.NoError(err)
	r.Empty(stranger.Data)
}

// TestFamilyPrefixFilterIsNotASubstringMatch pins the LIKE escaping. Family
// names contain `_`, which LIKE treats as "any character", so an unescaped
// pattern would make family filtering quietly wrong — and in the exclusion
// direction, quietly unsafe.
func TestFamilyPrefixFilterIsNotASubstringMatch(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	f := newFixture(t)

	base := time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC)
	f.seed(ctx, t, models.EventTypeOnCallScheduleCreated, nil, base, "")
	f.seed(ctx, t, models.EventTypeMemberInvited, nil, base.Add(time.Minute), "")
	// A decoy that an unescaped `oncall_schedule.%` pattern would also match.
	decoy := models.NewEvent(f.org.UID, models.EventType("oncallXschedule.created"), models.ActorTypeSystem)
	decoy.CreatedAt = base.Add(2 * time.Minute)
	r.NoError(f.db.CreateEvent(ctx, decoy))

	resp, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
		Size:              50,
		EventTypePrefixes: []string{"oncall_schedule"},
		Caller:            events.Caller{UserUID: f.admin.UID},
	})
	r.NoError(err)
	r.Equal([]string{"oncall_schedule.created"}, typesOf(resp))
}

// TestFamilyAndExactFiltersAreOred — asking for one exact type and one family
// must return the union, not the (empty) intersection.
func TestFamilyAndExactFiltersAreOred(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	f := newFixture(t)

	base := time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC)
	f.seed(ctx, t, models.EventTypeCheckCreated, nil, base, "")
	f.seed(ctx, t, models.EventTypeMemberInvited, nil, base.Add(time.Minute), "")
	f.seed(ctx, t, models.EventTypeIncidentCreated, nil, base.Add(2*time.Minute), "")

	resp, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
		Size:              50,
		EventTypes:        []string{"check.created"},
		EventTypePrefixes: []string{"member"},
		Caller:            events.Caller{UserUID: f.admin.UID},
	})
	r.NoError(err)
	r.ElementsMatch([]string{"check.created", "member.invited"}, typesOf(resp))
}

// TestActorFilterAndResolution covers the actorUserUid filter and the actor
// identity the audit table renders.
func TestActorFilterAndResolution(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	f := newFixture(t)

	base := time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC)
	f.seed(ctx, t, models.EventTypeCheckCreated, &f.admin.UID, base, "")
	f.seed(ctx, t, models.EventTypeCheckUpdated, &f.viewer.UID, base.Add(time.Minute), "")

	resp, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
		Size:     50,
		ActorUID: &f.viewer.UID,
		Caller:   events.Caller{UserUID: f.admin.UID},
	})
	r.NoError(err)
	r.Equal([]string{"check.updated"}, typesOf(resp))
	r.NotNil(resp.Data[0].ActorName)
	r.Equal("Viewer Person", *resp.Data[0].ActorName)
	r.NotNil(resp.Data[0].ActorEmail)
	r.Equal("viewer@acme.com", *resp.Data[0].ActorEmail)
}

// TestCursorPaginationWalksTheWholeTrail. Before this spec the endpoint handed
// out a bare UID that the service then ignored, so "next page" silently
// returned page 1 — an infinite loop for any client that trusted it.
//
// The load-bearing assertion is the last one: the two pages together cover
// every seeded event exactly once.
func TestCursorPaginationWalksTheWholeTrail(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	f := newFixture(t)

	base := time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC)

	seeded := make(map[string]bool)
	for i := 0; i < 5; i++ {
		event := f.seed(ctx, t, models.EventTypeCheckCreated, nil, base.Add(time.Duration(i)*time.Minute), "")
		seeded[event.UID] = true
	}

	first, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
		Size:   3,
		Caller: events.Caller{UserUID: f.admin.UID},
	})
	r.NoError(err)
	r.Len(first.Data, 3)
	r.NotEmpty(first.Pagination.Cursor)

	second, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
		Size:   3,
		Cursor: first.Pagination.Cursor,
		Caller: events.Caller{UserUID: f.admin.UID},
	})
	r.NoError(err)
	r.Len(second.Data, 2, "the second page must be the REST, not page one again")
	r.Empty(second.Pagination.Cursor)

	seen := map[string]int{}
	for _, item := range append(append([]events.EventResponse{}, first.Data...), second.Data...) {
		seen[item.UID]++
	}

	r.Len(seen, 5)

	for uid := range seeded {
		r.Equalf(1, seen[uid], "event %s must appear exactly once across the pages", uid)
	}
}

// TestCursorRoundTripAndGarbageTolerance. A malformed cursor is a client bug,
// not a reason to 500 — and the legacy bare-UID cursors that used to be handed
// out must degrade to "first page" rather than erroring.
func TestCursorRoundTripAndGarbageTolerance(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	at := time.Date(2026, time.August, 21, 9, 30, 15, 123456789, time.UTC)
	cursor := events.EncodeCursor(at, "abc-123")

	ts, uid, ok := events.DecodeCursor(cursor)
	r.True(ok)
	r.Equal("abc-123", uid)
	r.True(at.Equal(ts))

	for _, garbage := range []string{"", "not-base64!!", "YWJj", "1e5b9d20-0000-0000-0000-000000000000"} {
		_, _, ok := events.DecodeCursor(garbage)
		r.Falsef(ok, "%q must decode as 'no cursor', not as a usable position", garbage)
	}
}

// TestTimeRangeFilterIsApplied — since/until were declared on the options
// struct and the filter from the start but never parsed or passed, so a
// time-bounded audit query silently returned the whole trail.
func TestTimeRangeFilterIsApplied(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	f := newFixture(t)

	base := time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC)
	f.seed(ctx, t, models.EventTypeCheckCreated, nil, base, "")
	f.seed(ctx, t, models.EventTypeCheckUpdated, nil, base.Add(2*time.Hour), "")
	f.seed(ctx, t, models.EventTypeCheckDeleted, nil, base.Add(4*time.Hour), "")

	since := base.Add(time.Hour)
	until := base.Add(3 * time.Hour)

	resp, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
		Size:   50,
		Since:  &since,
		Until:  &until,
		Caller: events.Caller{UserUID: f.admin.UID},
	})
	r.NoError(err)
	r.Equal([]string{"check.updated"}, typesOf(resp))
}

// TestCursorPaginationSurvivesIdenticalTimestamps is the tie-break guard.
//
// The keyset predicate is `created_at < ? OR (created_at = ? AND uid < ?)`,
// which is only correct if rows sharing a created_at have a DEFINED order.
// With `ORDER BY created_at DESC` alone, the order among equal timestamps is
// whatever the engine feels like, so a page boundary landing inside a tied
// group could skip events entirely — silently losing audit rows, which is the
// worst possible failure for this table.
//
// TestCursorPaginationWalksTheWholeTrail seeds timestamps a minute apart and
// therefore never exercises the tie. This one makes every row share a single
// instant, which is exactly what a bulk insert (a config apply, an SSO
// backfill, a burst of failed logins) produces in the real table.
func TestCursorPaginationSurvivesIdenticalTimestamps(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	f := newFixture(t)

	// One instant, twelve events. Not "close together" — identical.
	at := time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC)

	seeded := make(map[string]bool)
	for i := 0; i < 12; i++ {
		event := f.seed(ctx, t, models.EventTypeCheckCreated, nil, at, "")
		seeded[event.UID] = true
	}

	r.Len(seeded, 12, "positive control: twelve distinct events were seeded")

	seen := map[string]int{}
	cursor := ""

	for page := 0; page < 10; page++ {
		resp, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
			Size:   5,
			Cursor: cursor,
			Caller: events.Caller{UserUID: f.admin.UID},
		})
		r.NoError(err)

		for _, item := range resp.Data {
			seen[item.UID]++
		}

		cursor = resp.Pagination.Cursor
		if cursor == "" {
			break
		}
	}

	r.Empty(cursor, "pagination must terminate")

	// The load-bearing assertion: nothing skipped, nothing duplicated.
	r.Lenf(seen, 12, "expected all 12 tied events, saw %d", len(seen))

	for uid := range seeded {
		r.Equalf(1, seen[uid],
			"event %s sharing a created_at with 11 others must appear exactly once", uid)
	}
}

// TestTargetFiltersNarrowToOneObject covers the `targetUid` / `targetType`
// filters. They are payload predicates rather than column ones because the
// target is polymorphic, so they are exactly the kind of thing that works on
// one engine and silently matches nothing on the other.
func TestTargetFiltersNarrowToOneObject(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	f := newFixture(t)

	base := time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC)

	f.seedWithPayload(ctx, t, models.EventTypeIntegrationUpdated, base,
		models.JSONMap{"target_type": "integration", "target_uid": "int-1"})
	f.seedWithPayload(ctx, t, models.EventTypeIntegrationDeleted, base.Add(time.Minute),
		models.JSONMap{"target_type": "integration", "target_uid": "int-2"})
	f.seedWithPayload(ctx, t, models.EventTypeMemberRemoved, base.Add(2*time.Minute),
		models.JSONMap{"target_type": "member", "target_uid": "user-9"})

	byUID, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
		Size:      50,
		TargetUID: strPtr("int-1"),
		Caller:    events.Caller{UserUID: f.admin.UID},
	})
	r.NoError(err)
	r.Equal([]string{"integration.updated"}, typesOf(byUID))

	byType, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
		Size:       50,
		TargetType: strPtr("integration"),
		Caller:     events.Caller{UserUID: f.admin.UID},
	})
	r.NoError(err)
	r.ElementsMatch([]string{"integration.updated", "integration.deleted"}, typesOf(byType))

	// Positive control: an unfiltered listing still returns all three, so the
	// two assertions above are testing the filters rather than an empty table.
	all, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
		Size:   50,
		Caller: events.Caller{UserUID: f.admin.UID},
	})
	r.NoError(err)
	r.Len(all.Data, 3)
}

// TestSourceIPFilterIsAdminOnly is the security half of the IP filter.
//
// Withholding the sourceIp COLUMN from a non-admin while honoring a sourceIp
// FILTER would leave the fact just as reachable: ask for an address, get a
// non-empty page, and you have confirmed a colleague works from it. So the
// service drops the predicate for a non-admin — and drops it SILENTLY, so the
// endpoint does not become an oracle for "am I an admin?" either.
//
// The positive control is the admin half: the filter genuinely narrows for
// someone allowed to use it, so this is not passing because the filter is
// broken for everyone.
func TestSourceIPFilterIsAdminOnly(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	f := newFixture(t)

	base := time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC)
	f.seed(ctx, t, models.EventTypeCheckCreated, &f.admin.UID, base, "203.0.113.7")
	f.seed(ctx, t, models.EventTypeCheckUpdated, &f.admin.UID, base.Add(time.Minute), "198.51.100.9")

	// Positive control: an admin's filter narrows.
	adminFiltered, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
		Size:     50,
		SourceIP: strPtr("203.0.113.7"),
		Caller:   events.Caller{UserUID: f.admin.UID},
	})
	r.NoError(err)
	r.Equal([]string{"check.created"}, typesOf(adminFiltered))

	// A non-admin's identical request is answered as if no IP filter had been
	// sent: both events come back, so no address is confirmed or denied.
	viewerFiltered, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
		Size:     50,
		SourceIP: strPtr("203.0.113.7"),
		Caller:   events.Caller{UserUID: f.viewer.UID},
	})
	r.NoError(err)
	r.Len(viewerFiltered.Data, 2,
		"a non-admin's IP filter must be ignored, not honored — otherwise it is an oracle")

	// And an address that matches NOTHING is answered the same way, so the
	// response size cannot be used to probe either.
	viewerMiss, err := f.svc.ListEvents(ctx, f.orgSlug, &events.ListEventsOptions{
		Size:     50,
		SourceIP: strPtr("192.0.2.1"),
		Caller:   events.Caller{UserUID: f.viewer.UID},
	})
	r.NoError(err)
	r.Len(viewerMiss.Data, 2, "a miss and a hit must be indistinguishable to a non-admin")

	// The column stays withheld regardless.
	for _, item := range viewerFiltered.Data {
		r.Nil(item.SourceIP)
	}
}

func strPtr(value string) *string { return &value }
