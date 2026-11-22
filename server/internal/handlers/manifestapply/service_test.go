package manifestapply_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
	"github.com/fclairamb/solidping/server/internal/handlers/maintenancewindows"
	"github.com/fclairamb/solidping/server/internal/handlers/manifestapply"
	"github.com/fclairamb/solidping/server/internal/handlers/statuspages"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/paramkeys"
	"github.com/fclairamb/solidping/server/internal/secretref"
)

const orgSlug = "acme"

type env struct {
	svc *manifestapply.Service
	db  db.Service
	org *models.Organization
}

func setup(t *testing.T) *env {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization(orgSlug, "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	creds, err := credentials.NewService(nil, credentials.NewParamStore(dbSvc))
	r.NoError(err)

	cfg := &config.Config{}
	ent := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	checksSvc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), creds, ent)
	checksSvc.SetEnvRefAllow(secretref.AllowEnvNames("ACME_NTFY_TOKEN"))

	svc := manifestapply.NewService(dbSvc, checksSvc,
		integrations.NewService(dbSvc, creds, nil, cfg),
		maintenancewindows.NewService(dbSvc),
		statuspages.NewService(dbSvc, cfg, ent))

	return &env{svc: svc, db: dbSvc, org: org}
}

func check(slug string) checks.ExportCheck {
	return checks.ExportCheck{
		Name: slug, Slug: slug, Type: "http", Enabled: true,
		Config: map[string]any{"url": "https://acme.example/" + slug},
	}
}

func channel(slug string) checks.ExportChannel {
	return checks.ExportChannel{
		Slug: slug, Name: "Alerts " + slug, Type: "ntfy",
		Settings: map[string]any{"topic": slug},
	}
}

func windowStart() time.Time { return time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC) }

func window(slug string, checkSlugs ...string) checks.ExportMaintenanceWindow {
	return checks.ExportMaintenanceWindow{
		Slug: slug, Title: "Window " + slug, StartAt: windowStart(), EndAt: windowStart().Add(time.Hour),
		Checks: checkSlugs,
	}
}

func page(slug string, sections ...checks.ExportStatusPageSection) checks.ExportStatusPage {
	return checks.ExportStatusPage{Slug: slug, Name: "Page " + slug, Visibility: "public", Sections: sections}
}

func manifest(name string) *checks.ExportDocument {
	return &checks.ExportDocument{Version: 2, Organization: name}
}

func (e *env) apply(t *testing.T, doc *checks.ExportDocument, opts checks.ApplyOptions) (*checks.ApplyResult, error) {
	t.Helper()

	return e.svc.Apply(t.Context(), orgSlug, doc, opts)
}

func (e *env) mustApply(t *testing.T, doc *checks.ExportDocument, opts checks.ApplyOptions) *checks.ApplyResult {
	t.Helper()

	res, err := e.apply(t, doc, opts)
	require.NoError(t, err)
	require.Empty(t, res.Errors)

	return res
}

func actions(res *checks.ApplyResult, kind string) map[string]string {
	out := map[string]string{}
	for _, entry := range res.Plan {
		if entry.Kind == kind {
			out[entry.Slug] = entry.Action
		}
	}

	return out
}

func (e *env) channels(t *testing.T) []*models.Integration {
	t.Helper()

	rows, err := e.db.ListChannels(t.Context(), &models.ListIntegrationsFilter{OrganizationUID: e.org.UID})
	require.NoError(t, err)

	return rows
}

func (e *env) windows(t *testing.T) []*models.MaintenanceWindow {
	t.Helper()

	rows, err := e.db.ListMaintenanceWindows(t.Context(), e.org.UID, models.ListMaintenanceWindowsFilter{})
	require.NoError(t, err)

	return rows
}

func (e *env) pages(t *testing.T) []*models.StatusPage {
	t.Helper()

	rows, err := e.db.ListStatusPages(t.Context(), e.org.UID)
	require.NoError(t, err)

	return rows
}

// kindCase drives the shared create/update/idempotency/prune checks for each kind.
type kindCase struct {
	name  string
	kind  string
	add   func(doc *checks.ExportDocument, slug string)
	edit  func(doc *checks.ExportDocument)
	count func(t *testing.T, e *env) int
	owner func(t *testing.T, e *env, slug string) *string
}

func kindCases() []kindCase {
	return []kindCase{
		{
			name: "channels", kind: checks.KindChannel,
			add: func(doc *checks.ExportDocument, slug string) { doc.Channels = append(doc.Channels, channel(slug)) },
			edit: func(doc *checks.ExportDocument) {
				doc.Channels[0].Name = "Renamed"
			},
			count: func(t *testing.T, e *env) int {
				t.Helper()

				return len(e.channels(t))
			},
			owner: func(t *testing.T, e *env, slug string) *string {
				t.Helper()

				for _, row := range e.channels(t) {
					if row.Slug != nil && *row.Slug == slug {
						return row.ManagedBy
					}
				}

				return nil
			},
		},
		{
			name: "maintenance windows", kind: checks.KindMaintenanceWindow,
			add: func(doc *checks.ExportDocument, slug string) {
				doc.MaintenanceWindows = append(doc.MaintenanceWindows, window(slug))
			},
			edit: func(doc *checks.ExportDocument) {
				doc.MaintenanceWindows[0].EndAt = windowStart().Add(2 * time.Hour)
			},
			count: func(t *testing.T, e *env) int {
				t.Helper()

				return len(e.windows(t))
			},
			owner: func(t *testing.T, e *env, slug string) *string {
				t.Helper()

				for _, row := range e.windows(t) {
					if row.Slug != nil && *row.Slug == slug {
						return row.ManagedBy
					}
				}

				return nil
			},
		},
		{
			name: "status pages", kind: checks.KindStatusPage,
			add: func(doc *checks.ExportDocument, slug string) {
				doc.StatusPages = append(doc.StatusPages, page(slug))
			},
			edit: func(doc *checks.ExportDocument) {
				doc.StatusPages[0].Name = "Renamed"
			},
			count: func(t *testing.T, e *env) int {
				t.Helper()

				return len(e.pages(t))
			},
			owner: func(t *testing.T, e *env, slug string) *string {
				t.Helper()

				for _, row := range e.pages(t) {
					if row.Slug == slug {
						return row.ManagedBy
					}
				}

				return nil
			},
		},
	}
}

func TestApplyPerKind(t *testing.T) {
	t.Parallel()

	for _, kc := range kindCases() {
		t.Run(kc.name+"/create update unchanged and managed marker", func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			e := setup(t)

			doc := manifest("prod")
			kc.add(doc, "one")

			res := e.mustApply(t, doc, checks.ApplyOptions{})
			r.Equal(1, res.Created)
			r.Equal(map[string]string{"one": checks.ApplyActionCreate}, actions(res, kc.kind))
			r.Equal(1, kc.count(t, e))
			owner := kc.owner(t, e, "one")
			r.NotNil(owner, "managed marker stamped")
			r.Equal("prod", *owner)

			// Idempotent second apply.
			res = e.mustApply(t, doc, checks.ApplyOptions{})
			r.Zero(res.Created)
			r.Zero(res.Updated)
			r.Equal(1, res.Unchanged)
			r.Equal(map[string]string{"one": checks.ApplyActionUnchanged}, actions(res, kc.kind))

			// Edit -> update.
			kc.edit(doc)
			res = e.mustApply(t, doc, checks.ApplyOptions{})
			r.Equal(1, res.Updated)
			r.Equal(map[string]string{"one": checks.ApplyActionUpdate}, actions(res, kc.kind))
			r.Equal(1, kc.count(t, e))
		})

		t.Run(kc.name+"/dry run mutates nothing", func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			e := setup(t)

			doc := manifest("prod")
			kc.add(doc, "one")

			res := e.mustApply(t, doc, checks.ApplyOptions{DryRun: true})
			r.Equal(1, res.Created)
			r.True(res.DryRun)
			r.Zero(kc.count(t, e))
		})

		t.Run(kc.name+"/prune needs the flag and never touches unmanaged", func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			e := setup(t)

			// Owned by "prod": one, two. Owned by another manifest: other.
			doc := manifest("prod")
			kc.add(doc, "one")
			kc.add(doc, "two")
			e.mustApply(t, doc, checks.ApplyOptions{})

			foreign := manifest("staging")
			kc.add(foreign, "other")
			e.mustApply(t, foreign, checks.ApplyOptions{})
			r.Equal(3, kc.count(t, e))

			// "two" dropped from the manifest: without prune it stays.
			doc2 := manifest("prod")
			kc.add(doc2, "one")

			res := e.mustApply(t, doc2, checks.ApplyOptions{})
			r.Zero(res.Deleted)
			r.Equal(3, kc.count(t, e))

			// Dry-run prune reports, writes nothing.
			res = e.mustApply(t, doc2, checks.ApplyOptions{Prune: true, DryRun: true})
			r.Equal(1, res.Deleted)
			r.Equal(checks.ApplyActionDelete, actions(res, kc.kind)["two"])
			r.Equal(3, kc.count(t, e))

			// Real prune deletes only the manifest's own absent row.
			res = e.mustApply(t, doc2, checks.ApplyOptions{Prune: true})
			r.Equal(1, res.Deleted)
			r.Equal(2, kc.count(t, e))
			r.Nil(kc.owner(t, e, "two"))
			r.NotNil(kc.owner(t, e, "other"), "another manifest's row survives")
		})

		t.Run(kc.name+"/deletion cap", func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			e := setup(t)

			doc := manifest("prod")
			kc.add(doc, "keep")
			for _, slug := range []string{"aaa", "bbb", "ccc"} {
				kc.add(doc, slug)
			}
			e.mustApply(t, doc, checks.ApplyOptions{})

			small := manifest("prod")
			kc.add(small, "keep")

			_, err := e.apply(t, small, checks.ApplyOptions{Prune: true, DeletionCap: 2})
			r.ErrorIs(err, checks.ErrDeletionCapExceeded)
			r.Equal(4, kc.count(t, e))

			e.mustApply(t, small, checks.ApplyOptions{Prune: true, DeletionCap: 2, Force: true})
			r.Equal(1, kc.count(t, e))
		})
	}
}

func TestApplyUnmanagedSlugIsReportedThenAdopted(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := setup(t)

	a := manifest("one")
	a.Channels = []checks.ExportChannel{channel("alerts")}
	e.mustApply(t, a, checks.ApplyOptions{})

	b := manifest("two")
	b.Channels = []checks.ExportChannel{channel("alerts")}
	b.Channels[0].Name = "Taken over"

	res := e.mustApply(t, b, checks.ApplyOptions{DryRun: true})
	r.Equal(map[string]string{"alerts": checks.ApplyActionUnmanaged}, actions(res, checks.KindChannel))
	r.Equal(1, res.Unmanaged)
}

func TestApplyAdoptsSlugLessRowByName(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := setup(t)
	ctx := t.Context()

	win := models.NewMaintenanceWindow(e.org.UID, "Window nightly", windowStart(), windowStart().Add(time.Hour))
	r.NoError(e.db.CreateMaintenanceWindow(ctx, win))

	doc := manifest("prod")
	doc.MaintenanceWindows = []checks.ExportMaintenanceWindow{window("nightly")}
	doc.MaintenanceWindows[0].Title = "Window nightly"

	e.mustApply(t, doc, checks.ApplyOptions{})
	r.Len(e.windows(t), 1, "no duplicate was created")
	r.Equal("nightly", *e.windows(t)[0].Slug)

	// A row with no slug and a name nobody names is never touched, even by prune.
	other := models.NewMaintenanceWindow(e.org.UID, "Untouched", windowStart(), windowStart().Add(time.Hour))
	r.NoError(e.db.CreateMaintenanceWindow(ctx, other))
	e.mustApply(t, doc, checks.ApplyOptions{Prune: true})
	r.Len(e.windows(t), 2)
}

func TestApplyEmptyAndNonCheckManifests(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := setup(t)

	_, err := e.apply(t, manifest("prod"), checks.ApplyOptions{})
	r.ErrorIs(err, checks.ErrEmptyChecksArray)

	doc := manifest("prod")
	doc.StatusPages = []checks.ExportStatusPage{page("status")}
	e.mustApply(t, doc, checks.ApplyOptions{})
	r.Len(e.pages(t), 1)
}

func TestApplyCrossKindManifest(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := setup(t)

	doc := manifest("prod")
	doc.Checks = []checks.ExportCheck{check("web"), check("api")}
	doc.Channels = []checks.ExportChannel{channel("alerts")}
	doc.MaintenanceWindows = []checks.ExportMaintenanceWindow{window("nightly", "web")}
	doc.StatusPages = []checks.ExportStatusPage{page("public",
		checks.ExportStatusPageSection{Slug: "core", Name: "Core", Checks: []string{"web", "api"}})}

	// Dry run first: the references to checks that do not exist yet resolve.
	res := e.mustApply(t, doc, checks.ApplyOptions{DryRun: true})
	r.Equal(2+1+1+1, res.Created)

	res = e.mustApply(t, doc, checks.ApplyOptions{})
	r.Equal(5, res.Created)

	// Plan is grouped per kind and every entry carries one.
	for _, entry := range res.Plan {
		r.NotEmpty(entry.Kind)
	}

	windows := e.windows(t)
	r.Len(windows, 1)

	assocs, err := e.db.ListMaintenanceWindowChecks(t.Context(), windows[0].UID)
	r.NoError(err)
	r.Len(assocs, 1)

	pageRow, err := e.db.GetStatusPageBySlug(t.Context(), e.org.UID, "public")
	r.NoError(err)

	sections, err := e.db.ListStatusPageSections(t.Context(), pageRow.UID)
	r.NoError(err)
	r.Len(sections, 1, "the default section is replaced by the manifest's")
	r.Equal("core", sections[0].Slug)

	resources, err := e.db.ListStatusPageResources(t.Context(), sections[0].UID)
	r.NoError(err)
	r.Len(resources, 2)

	// Second apply is a no-op on every kind.
	res = e.mustApply(t, doc, checks.ApplyOptions{})
	r.Zero(res.Created)
	r.Zero(res.Updated)
	r.Equal(5, res.Unchanged)

	// Dropping a check from the section is an update that removes the resource.
	doc.StatusPages[0].Sections[0].Checks = []string{"api"}
	res = e.mustApply(t, doc, checks.ApplyOptions{})
	r.Equal(map[string]string{"public": checks.ApplyActionUpdate}, actions(res, checks.KindStatusPage))

	resources, err = e.db.ListStatusPageResources(t.Context(), sections[0].UID)
	r.NoError(err)
	r.Len(resources, 1)
}

func TestApplyRejectsUnknownCheckReference(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := setup(t)

	doc := manifest("prod")
	doc.Channels = []checks.ExportChannel{channel("alerts")}
	doc.MaintenanceWindows = []checks.ExportMaintenanceWindow{window("nightly", "ghost")}

	_, err := e.apply(t, doc, checks.ApplyOptions{})
	r.ErrorIs(err, manifestapply.ErrUnknownCheckRef)
	r.Empty(e.channels(t), "nothing is written when a reference is unknown")
}

func TestApplyValidatesKeys(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := setup(t)

	doc := manifest("prod")
	doc.Channels = []checks.ExportChannel{channel("Bad Slug")}
	_, err := e.apply(t, doc, checks.ApplyOptions{})
	r.ErrorIs(err, manifestapply.ErrInvalidManifest)

	doc = manifest("prod")
	doc.Channels = []checks.ExportChannel{channel("dup"), channel("dup")}
	_, err = e.apply(t, doc, checks.ApplyOptions{})
	r.ErrorIs(err, manifestapply.ErrInvalidManifest)
}

// TestApplyChannelSecretReferences is not parallel: it sets an environment variable.
func TestApplyChannelSecretReferences(t *testing.T) {
	r := require.New(t)
	e := setup(t)

	t.Setenv("ACME_NTFY_TOKEN", "tk_super_secret_value")

	doc := manifest("prod")
	ch := channel("alerts")
	ch.Settings["auth_token"] = "${env:ACME_NTFY_TOKEN}"
	doc.Channels = []checks.ExportChannel{ch}

	res := e.mustApply(t, doc, checks.ApplyOptions{})
	r.Equal(1, res.Created)

	rows := e.channels(t)
	r.Len(rows, 1)

	// The secret reaches the encrypted private column, never the public one.
	for _, value := range rows[0].Settings {
		r.NotEqual("tk_super_secret_value", value)
	}
	r.NotNil(rows[0].SettingsPrivate)

	// A second apply recognizes the stored secret: nothing to do.
	res = e.mustApply(t, doc, checks.ApplyOptions{})
	r.Equal(map[string]string{"alerts": checks.ApplyActionUnchanged}, actions(res, checks.KindChannel))

	// A rotated secret is an update whose plan never prints either value.
	t.Setenv("ACME_NTFY_TOKEN", "tk_rotated_value")

	res = e.mustApply(t, doc, checks.ApplyOptions{DryRun: true})
	r.Equal(map[string]string{"alerts": checks.ApplyActionUpdate}, actions(res, checks.KindChannel))

	for _, entry := range res.Plan {
		for _, change := range entry.Changes {
			r.NotContains(change.From+change.To, "tk_")
		}
	}

	// A reference that does not resolve fails before any write.
	doc.Channels[0].Settings["auth_token"] = "${env:NOT_ALLOWED}"
	_, err := e.apply(t, doc, checks.ApplyOptions{})
	r.ErrorIs(err, checks.ErrUnresolvedSecretRef)
}

// TestApplyChannelRefusesReferenceInPublicKey: a reference to a secret org
// parameter under a key the type does not declare secret would land, resolved,
// in the public settings column. Both the dry run and the apply refuse it,
// without echoing the value, and nothing is written.
func TestApplyChannelRefusesReferenceInPublicKey(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := setup(t)
	ctx := t.Context()

	const leaked = "ps_leaked_value"

	r.NoError(e.db.SetOrgParameter(ctx, e.org.UID, paramkeys.StorageKey("acme_secret"), leaked, true))

	// Create path: an extra, undeclared key.
	doc := manifest("prod")
	ch := channel("alerts")
	ch.Settings["extra"] = "prefix-${param:acme_secret}"
	doc.Channels = []checks.ExportChannel{ch}

	for _, dry := range []bool{true, false} {
		_, err := e.apply(t, doc, checks.ApplyOptions{DryRun: dry})
		r.ErrorIs(err, manifestapply.ErrInvalidManifest)
		r.NotContains(err.Error(), leaked)
		r.Contains(err.Error(), "extra")
		r.Contains(err.Error(), "auth_token", "the error names the keys that take secrets")
	}

	r.Empty(e.channels(t))

	// Update path: a declared-public key of an existing channel.
	doc.Channels = []checks.ExportChannel{channel("alerts")}
	e.mustApply(t, doc, checks.ApplyOptions{})

	doc.Channels[0].Settings["topic"] = "${param:acme_secret}"

	for _, dry := range []bool{true, false} {
		res, err := e.apply(t, doc, checks.ApplyOptions{DryRun: dry})
		r.ErrorIs(err, manifestapply.ErrInvalidManifest)
		r.NotContains(err.Error(), leaked)
		r.Nil(res)
	}

	rows := e.channels(t)
	r.Len(rows, 1)
	r.Equal("alerts", rows[0].Settings["topic"])

	raw, err := json.Marshal(rows[0].Settings)
	r.NoError(err)
	r.NotContains(string(raw), leaked)
}

// TestApplyChannelPlainChangeStaysVisible: the masking does not hide an
// ordinary change of a non-secret key.
func TestApplyChannelPlainChangeStaysVisible(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := setup(t)

	doc := manifest("prod")
	doc.Channels = []checks.ExportChannel{channel("alerts")}
	e.mustApply(t, doc, checks.ApplyOptions{})

	doc.Channels[0].Settings["topic"] = "ops-2"
	res := e.mustApply(t, doc, checks.ApplyOptions{DryRun: true})

	var found bool

	for _, entry := range res.Plan {
		for _, change := range entry.Changes {
			if change.Field == "settings.topic" {
				found = true

				r.Equal("alerts", change.From)
				r.Equal("ops-2", change.To)
			}
		}
	}

	r.True(found, "the topic change is in the plan")
}

// TestExportOmitsChannelSecretReference: after an apply with a reference under
// a declared secret key, the export carries neither the value nor the key.
func TestExportOmitsChannelSecretReference(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := setup(t)
	ctx := t.Context()

	const secret = "ps_export_secret"

	r.NoError(e.db.SetOrgParameter(ctx, e.org.UID, paramkeys.StorageKey("ntfy_token"), secret, true))

	doc := manifest("prod")
	ch := channel("alerts")
	ch.Settings["auth_token"] = "${param:ntfy_token}"
	doc.Channels = []checks.ExportChannel{ch}
	e.mustApply(t, doc, checks.ApplyOptions{})

	exported, err := e.svc.Export(ctx, orgSlug)
	r.NoError(err)
	r.Len(exported.Channels, 1)
	r.NotContains(exported.Channels[0].Settings, "auth_token")

	raw, err := json.Marshal(exported)
	r.NoError(err)
	r.NotContains(string(raw), secret)
	r.NotContains(string(raw), "auth_token")
}

func TestExportBootstrapRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := setup(t)
	ctx := context.Background()

	// A live org: a check, a channel and a window made without apply, plus a page.
	_, err := e.svc.Export(ctx, "missing")
	r.Error(err)

	doc := manifest("seed")
	doc.Checks = []checks.ExportCheck{check("web")}
	doc.StatusPages = []checks.ExportStatusPage{page("public",
		checks.ExportStatusPageSection{Slug: "core", Name: "Core", Checks: []string{"web"}})}
	e.mustApply(t, doc, checks.ApplyOptions{})

	conn := models.NewIntegration(e.org.UID, models.ConnectionTypeNtfy, "Ops Alerts")
	conn.Settings = models.JSONMap{"topic": "ops"}
	r.NoError(e.db.CreateChannel(ctx, conn))

	win := models.NewMaintenanceWindow(e.org.UID, "Nightly Patching", windowStart(), windowStart().Add(time.Hour))
	r.NoError(e.db.CreateMaintenanceWindow(ctx, win))

	exported, err := e.svc.Export(ctx, orgSlug)
	r.NoError(err)
	r.Len(exported.Channels, 1)
	r.Equal("ops-alerts", exported.Channels[0].Slug)
	r.Len(exported.MaintenanceWindows, 1)
	r.Equal("nightly-patching", exported.MaintenanceWindows[0].Slug)
	r.Len(exported.StatusPages, 1)

	// The exported manifest applies back without creating duplicates.
	exported.Version = 2
	exported.Organization = "bootstrap"

	res := e.mustApply(t, exported, checks.ApplyOptions{})
	r.Zero(res.Created, "bootstrap manifest adopts what exists")
	r.Len(e.channels(t), 1)
	r.Len(e.windows(t), 1)
	r.Len(e.pages(t), 1)

	// And once adopted it is idempotent.
	exported2, err := e.svc.Export(ctx, orgSlug)
	r.NoError(err)

	exported2.Organization = "bootstrap"
	res = e.mustApply(t, exported2, checks.ApplyOptions{})
	r.Zero(res.Created)
	r.Zero(res.Updated)
}
