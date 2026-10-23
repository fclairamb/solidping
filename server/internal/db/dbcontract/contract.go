// Package dbcontract holds behavior tests that every db.Service dialect must
// pass identically. Each dialect's test package calls Run with its own
// service, so a query that SQLite accepts and Postgres refuses (or the
// reverse) fails in exactly one place. It is a plain (non-_test) package only
// because Go cannot import a _test file across packages; nothing in the server
// links it.
package dbcontract

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// fixture is the org, user and check every group of the contract builds on.
type fixture struct {
	svc   db.Service
	org   *models.Organization
	user  *models.User
	check *models.Check
}

func newFixture(t *testing.T, svc db.Service) *fixture {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)

	org := models.NewOrganization("contract-org", "Contract Org")
	must.NoError(svc.CreateOrganization(ctx, org))

	user := models.NewUser("contract@acme.com")
	must.NoError(svc.CreateUser(ctx, user))

	check := models.NewCheck(org.UID, "contract-check", "http")
	must.NoError(svc.CreateCheck(ctx, check))

	return &fixture{svc: svc, org: org, user: user, check: check}
}

// Run executes every contract group against svc, which must be freshly
// initialized and empty.
func Run(t *testing.T, svc db.Service) {
	t.Helper()

	fix := newFixture(t, svc)

	groups := []struct {
		name string
		run  func(*testing.T, *fixture)
	}{
		{"organization_slugs_and_membership", runOrganizations},
		{"severities", runSeverities},
		{"check_groups", runCheckGroups},
		{"maintenance_windows", runMaintenanceWindows},
		{"slos", runSLOs},
		{"report_schedules", runReportSchedules},
		{"escalation_policies", runEscalation},
		{"on_call", runOnCall},
		{"check_dependencies", runCheckDependencies},
		{"providers", runProviders},
		{"user_tokens", runUserTokens},
		{"passkeys", runPasskeys},
		{"system_parameters", runSystemParameters},
		{"entitlements", runEntitlements},
		{"status_pages", runStatusPages},
		{"check_connections", runCheckConnections},
		{"incident_members", runIncidentMembers},
		{"events_and_labels", runEventsAndLabels},
		{"bulk_results", runBulkResults},
		{"agents", runAgents},
	}

	for _, g := range groups {
		t.Run(g.name, func(t *testing.T) { g.run(t, fix) })
	}
}
