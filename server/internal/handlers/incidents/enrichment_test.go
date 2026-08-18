package incidents_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// newEnrichmentTestService spins up an in-memory SQLite db.Service plus the
// incidents.Service, mirroring newTotalTestService's pattern (total_test.go)
// — these tests call svc.ListIncidents / svc.GetIncident directly.
func newEnrichmentTestService(t *testing.T) (*sqlite.Service, *incidents.Service, *models.Organization) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	r.NoError(dbSvc.Initialize(ctx))

	org := models.NewOrganization("incidents-enrich", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clock.Real{}, nil)

	return dbSvc, svc, org
}

// newGroupIncidentWithMembers creates a check group with memberCount member
// checks, a group incident hanging off the FIRST member's check (mirroring
// TestAddComment_GroupIncidentFansOutToEveryMembersChannels's fixture shape
// in comment_fanout_test.go — the group incident's own CheckUID intentionally
// coincides with one of its members' CheckUID, the shape that would leak
// check fields under with=members if the with=check gate weren't checked
// independently of map presence), and links every member into the incident.
func newGroupIncidentWithMembers(
	t *testing.T, dbSvc *sqlite.Service, org *models.Organization, groupSlug string, memberCount int,
) *models.Incident {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	group := models.NewCheckGroup(org.UID, groupSlug, groupSlug)
	r.NoError(dbSvc.CreateCheckGroup(ctx, group))

	checks := make([]*models.Check, 0, memberCount)
	for i := range memberCount {
		check := models.NewCheck(org.UID, fmt.Sprintf("%s-member-%d", groupSlug, i), "http")
		check.CheckGroupUID = &group.UID
		r.NoError(dbSvc.CreateCheck(ctx, check))
		checks = append(checks, check)
	}

	inc := models.NewIncident(org.UID, checks[0].UID, time.Now().Add(-time.Minute), groupSlug+" is down")
	inc.CheckGroupUID = &group.UID
	r.NoError(dbSvc.CreateIncident(ctx, inc))

	now := time.Now()
	for _, check := range checks {
		r.NoError(dbSvc.UpsertIncidentMemberCheck(ctx, &models.IncidentMemberCheck{
			IncidentUID:    inc.UID,
			CheckUID:       check.UID,
			JoinedAt:       now,
			FirstFailureAt: now,
			LastFailureAt:  now,
			FailureCount:   1,
		}))
	}

	return inc
}

// newSoloIncident creates a plain (non-group) check with an active incident.
func newSoloIncident(
	t *testing.T, dbSvc *sqlite.Service, org *models.Organization, slug string,
) *models.Incident {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	check := models.NewCheck(org.UID, slug, "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	inc := models.NewIncident(org.UID, check.UID, time.Now().Add(-time.Minute), slug+" is down")
	r.NoError(dbSvc.CreateIncident(ctx, inc))

	return inc
}

// TestListIncidents_WithMembers_GatesMembersAndCheckGroupSlug proves
// `members`/`checkGroupSlug` appear on a group incident's list-page response
// only when `with=members` is requested, and are absent otherwise — the core
// gating decided by spec 2026-08-18-01 Proposal A.
func TestListIncidents_WithMembers_GatesMembersAndCheckGroupSlug(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	dbSvc, svc, org := newEnrichmentTestService(t)
	ctx := t.Context()

	newGroupIncidentWithMembers(t, dbSvc, org, "api-group", 2)

	// Default (no with=members): fields absent.
	resp, err := svc.ListIncidents(ctx, org.Slug, &incidents.ListIncidentsOptions{Size: 10})
	r.NoError(err)
	r.Len(resp.Data, 1)
	r.Nil(resp.Data[0].Members, "members must be absent without with=members")
	r.Nil(resp.Data[0].CheckGroupSlug, "checkGroupSlug must be absent without with=members")

	// with=members: fields present and correctly populated.
	resp, err = svc.ListIncidents(ctx, org.Slug, &incidents.ListIncidentsOptions{Size: 10, WithMembers: true})
	r.NoError(err)
	r.Len(resp.Data, 1)
	r.Len(resp.Data[0].Members, 2, "members must be populated with with=members")
	r.NotNil(resp.Data[0].CheckGroupSlug)
	r.Equal("api-group", *resp.Data[0].CheckGroupSlug)
}

// TestListIncidents_WithMembersOnly_DoesNotLeakCheckFields is the regression
// test for the gating bug caught while implementing batched enrichment: the
// batched checks query is SHARED between with=check (the incident's own
// check) and with=members (each member's check) to keep the query count
// down, so `enrichment.checks` can hold an entry for `inc.CheckUID` purely
// because it was fetched to resolve a member — even when with=check itself
// was never requested. The response must still gate `check`/`checkSlug`/
// `checkName` on with=check, not on map presence.
func TestListIncidents_WithMembersOnly_DoesNotLeakCheckFields(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	dbSvc, svc, org := newEnrichmentTestService(t)

	// The group incident's own CheckUID is checks[0].UID — also a member —
	// so the shared checks batch necessarily resolves it even though
	// with=check is NOT requested below.
	newGroupIncidentWithMembers(t, dbSvc, org, "api-group", 2)

	resp, err := svc.ListIncidents(t.Context(), org.Slug, &incidents.ListIncidentsOptions{
		Size: 10, WithMembers: true, // deliberately no WithCheck
	})
	r.NoError(err)
	r.Len(resp.Data, 1)
	r.Nil(resp.Data[0].Check, "with=members alone must not leak check details gated by with=check")
	r.Nil(resp.Data[0].CheckSlug, "with=members alone must not leak checkSlug gated by with=check")
	r.Nil(resp.Data[0].CheckName, "with=members alone must not leak checkName gated by with=check")
	r.Len(resp.Data[0].Members, 2, "members must still be populated")
}

// TestListIncidents_WithCheck_PopulatesCheckFields proves checkSlug /
// checkName / check keep their existing with=check-gated behavior on the
// list endpoint, unchanged by the members batching rework.
func TestListIncidents_WithCheck_PopulatesCheckFields(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	dbSvc, svc, org := newEnrichmentTestService(t)

	newSoloIncident(t, dbSvc, org, "api")

	// Default (no with=check): fields absent.
	resp, err := svc.ListIncidents(t.Context(), org.Slug, &incidents.ListIncidentsOptions{Size: 10})
	r.NoError(err)
	r.Len(resp.Data, 1)
	r.Nil(resp.Data[0].Check)
	r.Nil(resp.Data[0].CheckSlug)
	r.Nil(resp.Data[0].CheckName)

	// with=check: fields present.
	resp, err = svc.ListIncidents(t.Context(), org.Slug, &incidents.ListIncidentsOptions{Size: 10, WithCheck: true})
	r.NoError(err)
	r.Len(resp.Data, 1)
	r.NotNil(resp.Data[0].Check)
	r.NotNil(resp.Data[0].CheckSlug)
	r.Equal("api", *resp.Data[0].CheckSlug)
}

// TestGetIncident_WithMembers_GatesMembersAndCheckGroupSlug is the
// single-get counterpart of TestListIncidents_WithMembers_...: same gating,
// same fields, on GET /incidents/:uid.
func TestGetIncident_WithMembers_GatesMembersAndCheckGroupSlug(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	dbSvc, svc, org := newEnrichmentTestService(t)
	ctx := t.Context()

	groupIncident := newGroupIncidentWithMembers(t, dbSvc, org, "api-group", 2)

	// Default: fields absent.
	resp, err := svc.GetIncident(ctx, org.Slug, groupIncident.UID, &incidents.GetIncidentOptions{})
	r.NoError(err)
	r.Nil(resp.Members)
	r.Nil(resp.CheckGroupSlug)

	// with=members: fields present.
	resp, err = svc.GetIncident(ctx, org.Slug, groupIncident.UID, &incidents.GetIncidentOptions{WithMembers: true})
	r.NoError(err)
	r.Len(resp.Members, 2)
	r.NotNil(resp.CheckGroupSlug)
	r.Equal("api-group", *resp.CheckGroupSlug)
}

// TestGetIncident_WithCheck_PopulatesCheckField proves `check` keeps its
// existing with=check-gated behavior on the single-get endpoint, and that
// checkSlug/checkName stay unconditionally populated (pre-existing behavior,
// untouched by this spec — GetIncident always resolves its own check).
func TestGetIncident_WithCheck_PopulatesCheckField(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	dbSvc, svc, org := newEnrichmentTestService(t)
	ctx := t.Context()

	inc := newSoloIncident(t, dbSvc, org, "api")

	// Default: checkSlug/checkName always set; check absent.
	resp, err := svc.GetIncident(ctx, org.Slug, inc.UID, &incidents.GetIncidentOptions{})
	r.NoError(err)
	r.NotNil(resp.CheckSlug)
	r.Equal("api", *resp.CheckSlug)
	r.Nil(resp.Check)

	// with=check: check also populated.
	resp, err = svc.GetIncident(ctx, org.Slug, inc.UID, &incidents.GetIncidentOptions{WithCheck: true})
	r.NoError(err)
	r.NotNil(resp.Check)
}
