package db_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portSlackChannelForOrg is distinct from every other embedded-Postgres port
// in this package so the suites can run side by side.
const portSlackChannelForOrg = 15512

// testSlackChannelForOrgPrefersDefault proves the org-wide Slack lookup is
// deterministic. Every per-user Slack DM — the test button, escalation pages,
// operator notices — pages through whatever this returns, so an unordered
// LIMIT 1 meant the DM could land in a different workspace between two calls
// (spec 2026-09-18-02).
func testSlackChannelForOrgPrefersDefault(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)

	org := models.NewOrganization("slack-lookup-org", "Slack Lookup Org")
	r.NoError(svc.CreateOrganization(ctx, org))

	// Oldest first, and NOT the default: an unordered LIMIT 1 typically
	// returns this one, so the assertion below really discriminates.
	oldest := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "oldest")
	oldest.Enabled = true
	oldest.IsDefault = false
	oldest.CreatedAt = time.Now().Add(-2 * time.Hour)
	oldest.Settings = models.JSONMap{"team_id": "T-OLD"}
	r.NoError(svc.CreateChannel(ctx, oldest))

	preferred := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "preferred")
	preferred.Enabled = true
	preferred.IsDefault = true
	preferred.CreatedAt = time.Now().Add(-time.Hour)
	preferred.Settings = models.JSONMap{"team_id": "T-DEFAULT"}
	r.NoError(svc.CreateChannel(ctx, preferred))

	newest := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "newest")
	newest.Enabled = true
	newest.IsDefault = false
	newest.Settings = models.JSONMap{"team_id": "T-NEW"}
	r.NoError(svc.CreateChannel(ctx, newest))

	for range 3 {
		got, err := svc.GetSlackChannelForOrg(ctx, org.UID)
		r.NoError(err)
		r.Equal(preferred.UID, got.UID, "the default integration must win")
	}
}

// testSlackChannelForOrgOldestWhenNoDefault covers the tie-break: with no
// default flagged anywhere, the oldest row wins, stably.
func testSlackChannelForOrgOldestWhenNoDefault(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)

	org := models.NewOrganization("slack-nodef-org", "Slack No Default Org")
	r.NoError(svc.CreateOrganization(ctx, org))

	oldest := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "oldest")
	oldest.Enabled = true
	oldest.CreatedAt = time.Now().Add(-3 * time.Hour)
	oldest.Settings = models.JSONMap{"team_id": "T-OLD"}
	r.NoError(svc.CreateChannel(ctx, oldest))

	newer := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "newer")
	newer.Enabled = true
	newer.CreatedAt = time.Now().Add(-time.Hour)
	newer.Settings = models.JSONMap{"team_id": "T-NEW"}
	r.NoError(svc.CreateChannel(ctx, newer))

	got, err := svc.GetSlackChannelForOrg(ctx, org.UID)
	r.NoError(err)
	r.Equal(oldest.UID, got.UID)
}

func TestSlackChannelForOrgSQLite(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	svc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)

	t.Cleanup(func() { _ = svc.Close() })
	require.NoError(t, svc.Initialize(ctx))

	testSlackChannelForOrgPrefersDefault(ctx, t, svc)
	testSlackChannelForOrgOldestWhenNoDefault(ctx, t, svc)
}

// TestSlackChannelForOrgPostgres runs the identical matrix against real
// PostgreSQL: the two dialects carry parallel GetSlackChannelForOrg
// implementations and this is what keeps them from drifting.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction)
func TestSlackChannelForOrgPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping embedded-Postgres test in short mode")
	}

	ctx := t.Context()

	svc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true, Port: portSlackChannelForOrg, RunMode: "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	testSlackChannelForOrgPrefersDefault(ctx, t, svc)
	testSlackChannelForOrgOldestWhenNoDefault(ctx, t, svc)
}

// TestSlackChannelForOrgOrdersInBothDialects is the structural half of the
// guard. The behavioral suite above needs an embedded Postgres, which a
// -short run (and a developer machine out of SysV shared-memory segments)
// does not have — but the two dialects carry parallel hand-written queries and
// a missing ORDER BY in either one silently randomizes which workspace every
// Slack DM is posted to. So assert the clause directly, in both sources.
func TestSlackChannelForOrgOrdersInBothDialects(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for _, source := range []string{
		filepath.Join("postgres", "user_contact.go"),
		filepath.Join("sqlite", "user_contact.go"),
	} {
		content, err := os.ReadFile(filepath.Clean(source))
		r.NoError(err)

		start := strings.Index(string(content), "func (s *Service) GetSlackChannelForOrg(")
		r.GreaterOrEqualf(start, 0, "%s must define GetSlackChannelForOrg", source)

		body := string(content)[start:]
		if end := strings.Index(body, "\nfunc "); end > 0 {
			body = body[:end]
		}

		// Positive control: this really is the query body.
		r.Containsf(body, "ConnectionTypeSlack", "%s slice must be the real query", source)

		r.Containsf(body, `Order("is_default DESC", "created_at ASC")`,
			"%s must return the org's DEFAULT Slack integration, then the oldest: an "+
				"unordered LIMIT 1 sends the same user's DM to a different workspace "+
				"between two calls", source)
	}
}
