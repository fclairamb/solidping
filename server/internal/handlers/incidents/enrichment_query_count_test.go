package incidents_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

// queryCounter is a bun.QueryHook that records every query bun issues, so
// tests can assert both a total count and which tables were touched. Used to
// prove ListIncidents' enrichment is batched (spec 2026-08-18-01): a page's
// query count must stay constant as the page's incident/member count grows,
// and must be genuinely zero for member/group tables when with=members is
// not requested.
type queryCounter struct {
	total   int
	queries []string
}

func (c *queryCounter) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

func (c *queryCounter) AfterQuery(_ context.Context, event *bun.QueryEvent) {
	c.total++
	c.queries = append(c.queries, event.Query)
}

func (c *queryCounter) reset() {
	c.total = 0
	c.queries = nil
}

// countContaining returns how many recorded queries reference substr (e.g. a
// table name) — used to assert zero member/group queries ran.
func (c *queryCounter) countContaining(substr string) int {
	n := 0
	for _, q := range c.queries {
		if strings.Contains(q, substr) {
			n++
		}
	}
	return n
}

// seedIncidentFixture adds groupCount group incidents (membersPerGroup
// members each) plus soloCount plain incidents to org, all with unique
// slugs disambiguated by the salt prefix (so it can be called more than
// once against the same org to grow the fixture).
func seedIncidentFixture(
	t *testing.T, dbSvc *sqlite.Service, org *models.Organization, salt string, groupCount, membersPerGroup, soloCount int,
) {
	t.Helper()

	for g := range groupCount {
		newGroupIncidentWithMembers(t, dbSvc, org, fmt.Sprintf("%s-g%d", salt, g), membersPerGroup)
	}
	for i := range soloCount {
		newSoloIncident(t, dbSvc, org, fmt.Sprintf("%s-s%d", salt, i))
	}
}

// TestListIncidents_WithCheckMembers_ConstantQueryCount is the query-count
// proof for spec 2026-08-18-01 Proposal B: a page with group incidents,
// fetched with with=check,members, must cost the SAME number of queries no
// matter how many incidents/members are behind it.
//
// Positive control: the counter's total is asserted to be at least the
// number of queries the enrichment MUST issue (org lookup + list + count +
// checks + members + groups = 6) before the "stays constant" comparison —
// so if the hook silently never fired (total stuck at 0), this test fails
// here rather than passing vacuously on the equality check below.
func TestListIncidents_WithCheckMembers_ConstantQueryCount(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	dbSvc, svc, org := newEnrichmentTestService(t)
	ctx := t.Context()

	counter := &queryCounter{}
	dbSvc.DB().AddQueryHook(counter)

	// Small fixture: 2 group incidents (3 members each) + 3 solo incidents.
	seedIncidentFixture(t, dbSvc, org, "small", 2, 3, 3)

	counter.reset()
	resp, err := svc.ListIncidents(ctx, org.Slug, &incidents.ListIncidentsOptions{
		Size: 50, WithCheck: true, WithMembers: true,
	})
	r.NoError(err)
	r.Len(resp.Data, 5)

	smallCount := counter.total
	r.GreaterOrEqual(smallCount, 6,
		"positive control: the hook must have actually fired for org lookup + list + "+
			"count + checks + members + groups — a stuck-at-0 counter would make the "+
			"constant-count assertion below pass vacuously")

	// Grow the fixture substantially — more groups, more members per group,
	// more solo incidents — and issue the identical call shape again.
	seedIncidentFixture(t, dbSvc, org, "large", 4, 8, 12)

	counter.reset()
	resp, err = svc.ListIncidents(ctx, org.Slug, &incidents.ListIncidentsOptions{
		Size: 100, WithCheck: true, WithMembers: true,
	})
	r.NoError(err)
	r.Greater(len(resp.Data), 5, "fixture must actually have grown for this to prove anything")

	r.Equal(smallCount, counter.total,
		"query count must stay constant as the page's incident/member count grows — "+
			"an N+1 regression would make this grow with the fixture")
}

// TestListIncidents_WithoutWithMembers_IssuesNoMemberOrGroupQueries proves
// the other half of Proposal A: when with=members is not requested, ZERO
// queries touch incident_member_checks or check_groups, even though the
// page contains a group incident with members.
func TestListIncidents_WithoutWithMembers_IssuesNoMemberOrGroupQueries(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	dbSvc, svc, org := newEnrichmentTestService(t)
	ctx := t.Context()

	newGroupIncidentWithMembers(t, dbSvc, org, "api-group", 5)

	counter := &queryCounter{}
	dbSvc.DB().AddQueryHook(counter)

	resp, err := svc.ListIncidents(ctx, org.Slug, &incidents.ListIncidentsOptions{Size: 10, WithCheck: true})
	r.NoError(err)
	r.Len(resp.Data, 1)

	// Positive control: some queries must have run (org lookup + list + count
	// + checks) — otherwise the "zero member queries" assertions below would
	// pass even if the hook were dead.
	r.GreaterOrEqual(counter.total, 4, "positive control: the hook must have fired for the non-member queries")

	r.Zero(counter.countContaining("incident_member_checks"),
		"no incident_member_checks query must run without with=members")
	r.Zero(counter.countContaining("check_groups"),
		"no check_groups query must run without with=members")
}
