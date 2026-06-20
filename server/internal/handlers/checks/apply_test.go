package checks_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// setupApplyService builds a checks service backed by an in-memory SQLite DB.
// When withMasterKey is true the credentials service is enabled (real KEK);
// otherwise it runs in plaintext-fallback mode so the secret-ref warning path
// can be exercised.
func setupApplyService(t *testing.T, withMasterKey bool) (*checks.Service, db.Service, *models.Organization) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("apply-org", "Apply Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	var kek []byte
	if withMasterKey {
		kek = newKEK(t)
	}
	creds, err := credentials.NewService(kek, newMemDEKStore())
	r.NoError(err)

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), creds, entSvc)

	return svc, dbSvc, org
}

// manifestCheck is a small helper to build an ExportCheck for a manifest.
func manifestCheck(slug string) checks.ExportCheck {
	return checks.ExportCheck{
		Name:    slug,
		Slug:    slug,
		Type:    "http",
		Config:  map[string]any{"url": "https://example.com/" + slug},
		Enabled: true,
	}
}

func doc(org string, cs ...checks.ExportCheck) *checks.ExportDocument {
	return &checks.ExportDocument{Version: 1, Organization: org, Checks: cs}
}

// hasManagedLabel reports whether the check identified by slug carries the
// reserved managed label with the given manifest value.
func hasManagedLabel(
	ctx context.Context, t *testing.T, dbSvc db.Service, org *models.Organization, slug, manifest string,
) bool {
	t.Helper()
	r := require.New(t)

	check, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, slug)
	r.NoError(err)
	labels, err := dbSvc.GetLabelsForCheck(ctx, check.UID)
	r.NoError(err)
	for _, l := range labels {
		if l.Key == checks.ManagedLabelKey && l.Value == manifest {
			return true
		}
	}

	return false
}

// TestApplyPlanCreateUpdateUnmanaged covers the core plan computation: create
// (slug absent), update (slug present + managed), unmanaged (slug present
// without the managed label).
func TestApplyPlanCreateUpdateUnmanaged(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, _, org := setupApplyService(t, true)
	ctx := t.Context()

	// Pre-create a managed check ("api") and a hand-created unmanaged check ("manual").
	res, err := svc.ApplyChecks(ctx, org.Slug, doc("apply-org", manifestCheck("api")), checks.ApplyOptions{})
	r.NoError(err)
	r.Equal(1, res.Created)

	_, err = svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "manual", Slug: "manual", Type: "http",
		Config: map[string]any{"url": "https://example.com/manual"},
	})
	r.NoError(err)

	// Now apply a manifest with: api (update), web (create), manual (unmanaged).
	plan, err := svc.ApplyChecks(ctx, org.Slug, doc("apply-org",
		manifestCheck("api"), manifestCheck("web"), manifestCheck("manual"),
	), checks.ApplyOptions{DryRun: true})
	r.NoError(err)

	actions := map[string]string{}
	for _, e := range plan.Plan {
		actions[e.Slug] = e.Action
	}
	r.Equal(checks.ApplyActionUpdate, actions["api"])
	r.Equal(checks.ApplyActionCreate, actions["web"])
	r.Equal(checks.ApplyActionUnmanaged, actions["manual"])
	r.Equal(1, plan.Unmanaged)
}

// TestApplyDryRunMutatesNothing verifies a dry-run computes the plan but never
// touches the DB.
func TestApplyDryRunMutatesNothing(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	res, err := svc.ApplyChecks(ctx, org.Slug, doc("apply-org", manifestCheck("alpha"), manifestCheck("bravo")),
		checks.ApplyOptions{DryRun: true})
	r.NoError(err)
	r.True(res.DryRun)
	r.Len(res.Plan, 2)

	// Nothing was created.
	list, _, err := dbSvc.ListChecks(ctx, org.UID, &models.ListChecksFilter{})
	r.NoError(err)
	r.Empty(list, "dry-run must not create any checks")
}

// TestApplyStampsManagedLabel verifies a real apply stamps the reserved managed
// label on every owned check so a future apply recognizes the scope.
func TestApplyStampsManagedLabel(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	_, err := svc.ApplyChecks(ctx, org.Slug, doc("team-a", manifestCheck("alpha")), checks.ApplyOptions{})
	r.NoError(err)
	r.True(hasManagedLabel(ctx, t, dbSvc, org, "alpha", "team-a"))
}

// TestApplyDeleteByAbsenceRequiresPrune verifies a managed check absent from a
// later manifest is planned for deletion but NOT deleted unless prune is set.
func TestApplyDeleteByAbsenceRequiresPrune(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	// Seed two managed checks.
	_, err := svc.ApplyChecks(ctx, org.Slug, doc("team-a", manifestCheck("alpha"), manifestCheck("bravo")),
		checks.ApplyOptions{})
	r.NoError(err)

	// Re-apply without "b". Without prune, "b" is planned-delete but kept.
	res, err := svc.ApplyChecks(ctx, org.Slug, doc("team-a", manifestCheck("alpha")), checks.ApplyOptions{})
	r.NoError(err)
	r.Equal(0, res.Deleted)

	planned := planAction(res.Plan, "bravo")
	r.Equal(checks.ApplyActionDelete, planned)

	_, err = dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "bravo")
	r.NoError(err, "without prune the absent managed check must remain")

	// With prune, "b" is deleted.
	res, err = svc.ApplyChecks(ctx, org.Slug, doc("team-a", manifestCheck("alpha")),
		checks.ApplyOptions{Prune: true})
	r.NoError(err)
	r.Equal(1, res.Deleted)

	_, err = dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "bravo")
	r.Error(err, "prune must delete the absent managed check")
}

// TestApplyPruneNeverTouchesUnmanaged verifies hand-created checks (no managed
// label) are never deleted even with prune set.
func TestApplyPruneNeverTouchesUnmanaged(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	// Hand-created check, never in any manifest.
	_, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "manual", Slug: "manual", Type: "http",
		Config: map[string]any{"url": "https://example.com/manual"},
	})
	r.NoError(err)

	// Apply a managed check, then prune-apply an empty-of-it manifest.
	_, err = svc.ApplyChecks(ctx, org.Slug, doc("team-a", manifestCheck("alpha")), checks.ApplyOptions{})
	r.NoError(err)

	res, err := svc.ApplyChecks(ctx, org.Slug, doc("team-a", manifestCheck("keep")),
		checks.ApplyOptions{Prune: true})
	r.NoError(err)
	// Only the managed "a" should be deletable; "manual" is untouched.
	r.Equal(1, res.Deleted)

	_, err = dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "manual")
	r.NoError(err, "prune must never delete an unmanaged hand-created check")
}

// TestApplyDeletionCapRefusesOversizedPrune verifies the deletion cap refuses a
// prune that would delete more than the cap allows (unless forced).
func TestApplyDeletionCapRefusesOversizedPrune(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	// Seed three managed checks.
	_, err := svc.ApplyChecks(ctx, org.Slug, doc("team-a",
		manifestCheck("alpha"), manifestCheck("bravo"), manifestCheck("charlie")), checks.ApplyOptions{})
	r.NoError(err)

	// Re-apply an empty-ish manifest with a deletion cap of 1 — 3 deletes > cap.
	_, err = svc.ApplyChecks(ctx, org.Slug, doc("team-a", manifestCheck("keep")),
		checks.ApplyOptions{Prune: true, DeletionCap: 1})
	r.Error(err)
	r.ErrorIs(err, checks.ErrDeletionCapExceeded)

	// All three managed checks must still exist (cap refused before mutating).
	for _, slug := range []string{"alpha", "bravo", "charlie"} {
		_, gErr := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, slug)
		r.NoError(gErr, "deletion-cap refusal must not delete %s", slug)
	}

	// Force lifts the cap.
	res, err := svc.ApplyChecks(ctx, org.Slug, doc("team-a", manifestCheck("keep")),
		checks.ApplyOptions{Prune: true, DeletionCap: 1, Force: true})
	r.NoError(err)
	r.Equal(3, res.Deleted)
}

// TestApplyResolvesEnvSecretRef verifies ${env:NAME} resolution feeds the value
// into the encrypted envelope (config_private), keeping the public config clean.
// Uses t.Setenv, which is incompatible with t.Parallel.
func TestApplyResolvesEnvSecretRef(t *testing.T) {
	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	t.Setenv("SP_TEST_APPLY_TOKEN", "s3cr3t-token")

	c := manifestCheck("secured")
	c.Config = map[string]any{"url": "https://example.com", "password": "${env:SP_TEST_APPLY_TOKEN}"}

	res, err := svc.ApplyChecks(ctx, org.Slug, doc("team-a", c), checks.ApplyOptions{})
	r.NoError(err)
	r.Equal(1, res.Created)
	r.Empty(res.Warnings, "master key set: no plaintext warning expected")

	row, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "secured")
	r.NoError(err)
	r.NotContains(row.Config, "password", "resolved secret must not land in public config")
	r.NotNil(row.ConfigPrivate)
	r.NotEmpty(*row.ConfigPrivate)
}

// TestApplyResolvesParamSecretRef verifies ${param:KEY} resolution against the
// org parameters table (org-scoped takes precedence over system-wide).
func TestApplyResolvesParamSecretRef(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	r.NoError(dbSvc.SetSystemParameter(ctx, "shared_token", "system-value", true))
	r.NoError(dbSvc.SetOrgParameter(ctx, org.UID, "shared_token", "org-value", true))

	c := manifestCheck("param-check")
	c.Config = map[string]any{"url": "https://example.com", "password": "${param:shared_token}"}

	res, err := svc.ApplyChecks(ctx, org.Slug, doc("team-a", c), checks.ApplyOptions{})
	r.NoError(err)
	r.Equal(1, res.Created)

	// The resolved value (org-scoped wins) is enveloped; we can't read plaintext
	// from the public side, but we confirm it's not leaked there.
	row, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "param-check")
	r.NoError(err)
	r.NotContains(row.Config, "password")
	r.NotNil(row.ConfigPrivate)
}

// TestApplyMissingSecretRefIsHardError verifies an unresolvable reference fails
// the apply before any mutation.
func TestApplyMissingSecretRefIsHardError(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	c := manifestCheck("broken")
	c.Config = map[string]any{"url": "https://example.com", "password": "${env:DEFINITELY_NOT_SET_XYZ}"}

	_, err := svc.ApplyChecks(ctx, org.Slug, doc("team-a", c), checks.ApplyOptions{})
	r.Error(err)
	r.ErrorIs(err, checks.ErrUnresolvedSecretRef)

	// Nothing was created.
	list, _, err := dbSvc.ListChecks(ctx, org.UID, &models.ListChecksFilter{})
	r.NoError(err)
	r.Empty(list, "a missing secret ref must fail closed before mutation")
}

// TestApplySecretRefPlaintextFallbackWarns verifies that when the master key is
// unset, resolving a secret ref emits a warning (not a refusal).
// Uses t.Setenv, which is incompatible with t.Parallel.
func TestApplySecretRefPlaintextFallbackWarns(t *testing.T) {
	r := require.New(t)
	svc, _, org := setupApplyService(t, false) // no master key → plaintext fallback
	ctx := t.Context()

	t.Setenv("SP_TEST_PLAINTEXT_TOKEN", "exposed")

	c := manifestCheck("warned")
	c.Config = map[string]any{"url": "https://example.com", "password": "${env:SP_TEST_PLAINTEXT_TOKEN}"}

	res, err := svc.ApplyChecks(ctx, org.Slug, doc("team-a", c), checks.ApplyOptions{})
	r.NoError(err)
	r.NotEmpty(res.Warnings, "plaintext fallback must warn when a secret ref is resolved")
}

// TestApplyExportRoundTripIsIdempotent verifies that applying an exported
// document is a no-op on the second run (apply of an export round-trips).
func TestApplyExportRoundTripIsIdempotent(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, _, org := setupApplyService(t, true)
	ctx := t.Context()

	// First apply: creates two checks under manifest "team-a".
	_, err := svc.ApplyChecks(ctx, org.Slug, doc("team-a", manifestCheck("one"), manifestCheck("two")),
		checks.ApplyOptions{})
	r.NoError(err)

	// Export, then re-apply the exported document with the same manifest name.
	exported, err := svc.ExportChecks(ctx, org.Slug, checks.ListChecksOptions{})
	r.NoError(err)
	exported.Organization = "team-a"

	res, err := svc.ApplyChecks(ctx, org.Slug, exported, checks.ApplyOptions{DryRun: true})
	r.NoError(err)
	r.Equal(0, res.Created, "round-tripped export must not create new checks")
	r.Equal(2, res.Updated, "round-tripped export must reconcile as updates, not creates")
	r.Equal(0, res.Unmanaged, "round-tripped checks carry the managed label")
	for _, e := range res.Plan {
		r.Equal(checks.ApplyActionUpdate, e.Action, "slug %s should be an update", e.Slug)
	}
}

// TestApplyRenameViaPreviousSlug verifies an explicit previousSlug reconciles a
// rename in place rather than delete+create.
func TestApplyRenameViaPreviousSlug(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, _, org := setupApplyService(t, true)
	ctx := t.Context()

	_, err := svc.ApplyChecks(ctx, org.Slug, doc("team-a", manifestCheck("old-name")), checks.ApplyOptions{})
	r.NoError(err)

	renamed := manifestCheck("new-name")
	renamed.PreviousSlug = "old-name"

	res, err := svc.ApplyChecks(ctx, org.Slug, doc("team-a", renamed), checks.ApplyOptions{DryRun: true})
	r.NoError(err)
	r.Equal(checks.ApplyActionRename, planAction(res.Plan, "new-name"))
	// The previous slug must NOT show up as a delete (the rename consumes it).
	r.Empty(planAction(res.Plan, "old-name"))
}

// planAction returns the planned action for a slug, or "" if absent.
func planAction(plan []checks.ApplyPlanEntry, slug string) string {
	for _, e := range plan {
		if e.Slug == slug {
			return e.Action
		}
	}

	return ""
}
