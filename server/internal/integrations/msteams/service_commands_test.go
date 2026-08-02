package msteams

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// linkedTenant sets up an org whose tenant is linked and whose bot has one
// captured channel, which is the state every command below assumes.
func linkedTenant(ctx context.Context, t *testing.T, svc *Service, slug string) *models.Organization {
	t.Helper()

	orgUID, code := startLinkFor(ctx, t, svc, slug)

	_, err := svc.LinkTenant(ctx, code, messageActivity(testTenantID, "19:channel-a", ""))
	require.NoError(t, err)

	org, err := svc.db.GetOrganization(ctx, orgUID)
	require.NoError(t, err)

	return org
}

// runCommand dispatches an @mention command and returns the connector's
// recorded replies.
func runCommand(ctx context.Context, t *testing.T, svc *Service, text string) {
	t.Helper()

	require.NoError(t, DispatchActivity(ctx, svc,
		messageActivity(testTenantID, "19:channel-a", "<at>SolidPing</at> "+text)))
}

// TestCommand_ChecksAddCreatesRealCheck is the round-trip the previous test
// harness could not run at all: it wired checksService to nil, so every
// check-mutating command was structurally untestable.
func TestCommand_ChecksAddCreatesRealCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	org := linkedTenant(ctx, t, svc, "teams-cmd-add")

	runCommand(ctx, t, svc, "checks add https://example.com -slug my-check")

	calls := connector.recorded()
	r.NotEmpty(calls)
	r.Contains(cardJSON(t, calls[len(calls)-1]), "added")

	// The check really exists in the org the tenant is linked to.
	check, err := svc.db.GetCheckByUidOrSlug(ctx, org.UID, "my-check")
	r.NoError(err)
	r.NotNil(check)
	r.Equal("https://example.com", check.Config["url"])
}

// TestCommand_ChecksAddAddsMissingScheme covers the bare-hostname shorthand.
func TestCommand_ChecksAddAddsMissingScheme(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	org := linkedTenant(ctx, t, svc, "teams-cmd-scheme")

	runCommand(ctx, t, svc, "checks add example.org -slug bare-host")

	check, err := svc.db.GetCheckByUidOrSlug(ctx, org.UID, "bare-host")
	r.NoError(err)
	r.NotNil(check)
	r.Equal("https://example.org", check.Config["url"])
}

// TestCommand_ChecksListShowsCreatedChecks exercises the read path.
func TestCommand_ChecksListShowsCreatedChecks(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	linkedTenant(ctx, t, svc, "teams-cmd-list")

	runCommand(ctx, t, svc, "checks add https://example.com -slug listed-check")
	runCommand(ctx, t, svc, "checks list")

	calls := connector.recorded()
	r.NotEmpty(calls)
	r.Contains(cardJSON(t, calls[len(calls)-1]), "listed-check")
}

// TestCommand_ChecksListEmpty covers the empty state.
func TestCommand_ChecksListEmpty(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	linkedTenant(ctx, t, svc, "teams-cmd-empty")

	runCommand(ctx, t, svc, "checks list")

	r.True(connector.containsText("No checks found"))
}

// TestCommand_ChecksRemoveDeletesCheck closes the create/delete loop.
func TestCommand_ChecksRemoveDeletesCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	org := linkedTenant(ctx, t, svc, "teams-cmd-rm")

	runCommand(ctx, t, svc, "checks add https://example.com -slug doomed-check")

	check, err := svc.db.GetCheckByUidOrSlug(ctx, org.UID, "doomed-check")
	r.NoError(err)
	r.NotNil(check)

	runCommand(ctx, t, svc, "checks rm doomed-check")

	calls := connector.recorded()
	r.Contains(cardJSON(t, calls[len(calls)-1]), "removed")

	gone, err := svc.db.GetCheckByUidOrSlug(ctx, org.UID, "doomed-check")
	r.True(err != nil || gone == nil, "the check must be gone after rm")
}

// TestCommand_ChecksRemoveUnknownSlug covers the error path.
func TestCommand_ChecksRemoveUnknownSlug(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	linkedTenant(ctx, t, svc, "teams-cmd-rm404")

	runCommand(ctx, t, svc, "checks rm no-such-check")

	calls := connector.recorded()
	r.Contains(cardJSON(t, calls[len(calls)-1]), "Failed to remove check")
}

// TestCommand_ResultsForKnownCheck exercises the results command against a
// real, existing check — proving the tenant → org → check resolution chain
// works end to end rather than erroring out.
func TestCommand_ResultsForKnownCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	linkedTenant(ctx, t, svc, "teams-cmd-res")

	runCommand(ctx, t, svc, "checks add https://example.com -slug res-check")
	runCommand(ctx, t, svc, "results -check res-check")

	calls := connector.recorded()
	r.NotEmpty(calls)

	last := calls[len(calls)-1].Activity.Text
	r.Contains(last, "res-check")
	r.NotContains(last, "not found", "the check must resolve, whatever its result history")
}

// TestCommand_ResultsUnknownCheck covers the not-found path.
func TestCommand_ResultsUnknownCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	linkedTenant(ctx, t, svc, "teams-cmd-res404")

	runCommand(ctx, t, svc, "results -check nope")

	calls := connector.recorded()
	r.Contains(cardJSON(t, calls[len(calls)-1]), "not found")
}

// TestCommand_ChecksAddRequiresURL covers the usage error.
func TestCommand_ChecksAddRequiresURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, connector := setupService(t)

	linkedTenant(ctx, t, svc, "teams-cmd-usage")

	runCommand(ctx, t, svc, "checks add")

	calls := connector.recorded()
	r.Contains(cardJSON(t, calls[len(calls)-1]), "Missing URL")
}

// TestCommand_ChecksAreScopedToTheLinkedOrg is the tenancy control for the
// command surface: a check created from one tenant must not appear in another
// organization.
func TestCommand_ChecksAreScopedToTheLinkedOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	org := linkedTenant(ctx, t, svc, "teams-cmd-scope")

	other := models.NewOrganization("teams-cmd-other", "")
	r.NoError(svc.db.CreateOrganization(ctx, other))

	runCommand(ctx, t, svc, "checks add https://example.com -slug scoped-check")

	mine, err := svc.db.GetCheckByUidOrSlug(ctx, org.UID, "scoped-check")
	r.NoError(err)
	r.NotNil(mine)

	theirs, err := svc.db.GetCheckByUidOrSlug(ctx, other.UID, "scoped-check")
	r.True(err != nil || theirs == nil, "the check must not be visible to another org")
}
