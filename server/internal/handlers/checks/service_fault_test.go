package checks_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

type checksFaultWorld struct {
	svc   *checks.Service
	dbSvc db.Service
	ctx   context.Context //nolint:containedctx // test fixture
	hook  *testsupport.FaultHook
	n     atomic.Int64
}

func newChecksFaultWorld(t *testing.T) *checksFaultWorld {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)

	return &checksFaultWorld{
		svc:   checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc),
		dbSvc: dbSvc,
		ctx:   ctx,
		hook:  testsupport.InstallFaults(dbSvc.DB()),
	}
}

// org seeds a fresh organization (unarmed).
func (w *checksFaultWorld) org(t *testing.T) *models.Organization {
	t.Helper()

	org := models.NewOrganization(fmt.Sprintf("cfault-%d", w.n.Add(1)), "Checks fault")
	require.NoError(t, w.dbSvc.CreateOrganization(w.ctx, org))

	return org
}

// check seeds an organization holding one http check (unarmed).
func (w *checksFaultWorld) check(t *testing.T) (*models.Organization, checks.CheckResponse) {
	t.Helper()

	org := w.org(t)
	req := httpCheckReq()
	req.Name = "seed"
	req.Labels = map[string]string{"env": "prod"}

	created, err := w.svc.CreateCheck(w.ctx, org.Slug, req)
	require.NoError(t, err)

	return org, created
}

// TestChecksServiceSurvivesAFailingQuery replays happy-path service operations
// once per database query, failing a different one each time: a failed store
// must surface as an error, never a panic.
func TestChecksServiceSurvivesAFailingQuery(t *testing.T) {
	t.Parallel()

	names := make([]string, 0, 20)
	for name := range checksFaultScenarios(t, newChecksFaultWorld(t)) {
		names = append(names, name)
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newChecksFaultWorld(t)
			w.hook.Sweep(t, checksFaultScenarios(t, w)[name])
		})
	}
}

func checksFaultScenarios(t *testing.T, w *checksFaultWorld) map[string]func(arm func()) {
	t.Helper()

	str := func(s string) *string { return &s }
	flag := func(b bool) *bool { return &b }

	return map[string]func(arm func()){
		"create": func(arm func()) {
			org := w.org(t)
			req := httpCheckReq()
			req.Name = "created"
			req.Labels = map[string]string{"team": "core"}
			arm()
			_, _ = w.svc.CreateCheck(w.ctx, org.Slug, req)
		},
		"get": func(arm func()) {
			org, created := w.check(t)
			arm()
			_, _ = w.svc.GetCheck(w.ctx, org.Slug, created.UID, checks.GetCheckOptions{
				IncludeLastResult: true, IncludeLastStatusChange: true, IncludeRegionFreshness: true,
			})
		},
		"list": func(arm func()) {
			org, _ := w.check(t)
			arm()
			_, _ = w.svc.ListChecks(w.ctx, org.Slug, checks.ListChecksOptions{
				IncludeLastResult: true, IncludeLastStatusChange: true,
				Labels: map[string]string{"env": "prod"}, Limit: 10,
			})
		},
		"list-sorted": func(arm func()) {
			org, _ := w.check(t)
			arm()
			_, _ = w.svc.ListChecks(w.ctx, org.Slug, checks.ListChecksOptions{Sort: "group", Query: "seed", Limit: 1})
		},
		"update": func(arm func()) {
			org, created := w.check(t)
			labels := map[string]string{"env": "staging"}
			arm()
			_, _ = w.svc.UpdateCheck(w.ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{
				Name: str("renamed"), Description: str("d"), Labels: &labels, Period: str("2m"),
			})
		},
		"update-config": func(arm func()) {
			org, created := w.check(t)
			cfg := map[string]any{"url": "https://example.org"}
			arm()
			_, _ = w.svc.UpdateCheck(w.ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &cfg})
		},
		"disable": func(arm func()) {
			org, created := w.check(t)
			arm()
			_, _ = w.svc.UpdateCheck(w.ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{
				Enabled: flag(false), ResolveOpenIncidents: flag(true),
			})
		},
		"upsert-create": func(arm func()) {
			org := w.org(t)
			req := &checks.UpsertCheckRequest{
				Name: "up", Type: "http", Config: map[string]any{"url": "https://example.com"},
			}
			arm()
			_, _, _ = w.svc.UpsertCheck(w.ctx, org.Slug, "up-slug-check", req)
		},
		"upsert-update": func(arm func()) {
			org, created := w.check(t)
			req := &checks.UpsertCheckRequest{
				Name: "again", Type: "http", Config: map[string]any{"url": "https://example.net"},
			}
			arm()
			_, _, _ = w.svc.UpsertCheck(w.ctx, org.Slug, *created.Slug, req)
		},
		"delete": func(arm func()) {
			org, created := w.check(t)
			arm()
			_ = w.svc.DeleteCheck(w.ctx, org.Slug, created.UID)
		},
		"export": func(arm func()) {
			org, _ := w.check(t)
			arm()
			_, _ = w.svc.ExportChecks(w.ctx, org.Slug, checks.ListChecksOptions{})
		},
		"stats": func(arm func()) {
			org, _ := w.check(t)
			arm()
			_, _ = w.svc.GetCheckStats(w.ctx, org.Slug)
		},
		"versions": func(arm func()) {
			org, created := w.check(t)
			arm()
			_, _ = w.svc.ListCheckVersions(w.ctx, org.Slug, created.UID, 10)
			_, _ = w.svc.GetCheckVersion(w.ctx, org.Slug, created.UID, 1)
			_, _ = w.svc.DiffCheckVersion(w.ctx, org.Slug, created.UID, 1, nil)
		},
		"restore-version": func(arm func()) {
			org, created := w.check(t)
			_, err := w.svc.UpdateCheck(w.ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Name: str("v2")})
			require.NoError(t, err)
			arm()
			_, _ = w.svc.RestoreCheckVersion(w.ctx, org.Slug, created.UID, 1)
		},
		"rotate-heartbeat-token": func(arm func()) {
			org := w.org(t)
			created, err := w.svc.CreateCheck(w.ctx, org.Slug, checks.CreateCheckRequest{
				Name: "hb", Type: "heartbeat", Config: map[string]any{},
			})
			require.NoError(t, err)
			arm()
			_, _ = w.svc.RotateHeartbeatToken(w.ctx, org.Slug, created.UID)
		},
	}
}
