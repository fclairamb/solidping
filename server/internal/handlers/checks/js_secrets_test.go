package checks_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkjs"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/paramkeys"
	"github.com/fclairamb/solidping/server/internal/secretref"
)

// The two-map split of spec 2026-09-11-05, proven in both directions: `env` is
// public (returned on GET, exported, diffable) and `secrets` is not (encrypted,
// never returned, stripped from export, preserved when an import omits it).
//
// A test that only asserted the `secrets` half would pass just as well if the
// implementation had encrypted BOTH maps — which is precisely the design the
// spec rejects — so every assertion below comes in a pair.

// jsSecretNeedle is the fixture password these tests store. It is registered in
// leakNeedles (leak_guard_test.go) so the package-wide grep fails any test here
// that lets it reach a public config or a job row.
const jsSecretNeedle = "js-fixture-password"

// jsEnvRefNeedle is the value behind the `${env:}` reference fixture. Also a
// leak needle: an env-backed secret that got resolved at WRITE time would land
// in the public config exactly like a param-backed one.
const jsEnvRefNeedle = "js-fixture-env-secret"

// jsScript reads both maps and reports what it found, so a result proves what
// the runtime actually received rather than what the row looks like.
const jsScript = `return { status: "up", output: {
  secret: secrets.PASSWORD,
  base: env.BASE_URL,
} };`

type jsRig struct {
	svc   *checks.Service
	dbSvc *sqlite.Service
	creds credentials.Service
	org   *models.Organization
}

func newJSRig(t *testing.T) *jsRig {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))

	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("js-secrets", "JS Secrets Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// Registered after the Close cleanup so it greps while the DB is open.
	registerLeakGuard(t, dbSvc, org.UID)

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	return &jsRig{
		svc:   checks.NewService(dbSvc, nil, creds, nil),
		dbSvc: dbSvc,
		creds: creds,
		org:   org,
	}
}

// effectiveConfig rebuilds the config the worker executes: public ∪ decrypted
// private, the same merge checkjobsvc performs at the claim boundary.
func (rig *jsRig) effectiveConfig(t *testing.T, slug string) map[string]any {
	t.Helper()
	r := require.New(t)

	row, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, slug)
	r.NoError(err)
	r.NotNil(row.ConfigPrivate, "a check with secrets must carry an envelope")

	private, err := rig.creds.DecryptForOrg(t.Context(), rig.org.UID, *row.ConfigPrivate)
	r.NoError(err)

	return credentials.MergeConfig(row.Config, private)
}

// runJS executes the js checker over an effective config, returning its output.
func runJS(t *testing.T, config map[string]any) map[string]any {
	t.Helper()
	r := require.New(t)

	cfg := &checkjs.JSConfig{}
	r.NoError(cfg.FromMap(config))

	result, err := (&checkjs.JSChecker{}).Execute(t.Context(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)

	return result.Output
}

func (rig *jsRig) createJSCheck(t *testing.T, slug string) checks.CheckResponse {
	t.Helper()

	period := "5m"

	created, err := rig.svc.CreateCheck(t.Context(), rig.org.Slug, checks.CreateCheckRequest{
		Name: "JS " + slug,
		Slug: slug,
		Type: "js",
		Config: map[string]any{
			"script":  jsScript,
			"env":     map[string]any{"BASE_URL": "https://acme.com"},
			"secrets": map[string]any{"PASSWORD": jsSecretNeedle},
		},
		Period: &period,
	})
	require.NoError(t, err)

	return created
}

// TestJSGetReturnsEnvButNotSecrets is the read-side half of the split.
func TestJSGetReturnsEnvButNotSecrets(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	rig := newJSRig(t)
	ctx := t.Context()

	rig.createJSCheck(t, "js-split")

	got, err := rig.svc.GetCheck(ctx, rig.org.Slug, "js-split", checks.GetCheckOptions{})
	r.NoError(err)

	// The secret half: absent from the response, advertised as stored.
	r.NotContains(got.Config, "secrets", "GET must never return the secrets map")
	r.Contains(got.ConfigPrivateKeys, "secrets")

	// The public half: present, with its VALUES — this is the direction that
	// fails if `env` were declared secret too.
	env, ok := got.Config["env"].(map[string]any)
	r.True(ok, "GET must return the env map, got %#v", got.Config["env"])
	r.Equal("https://acme.com", env["BASE_URL"])

	// And at rest: the public column holds env, the envelope holds secrets.
	row, err := rig.dbSvc.GetCheckByUidOrSlug(ctx, rig.org.UID, "js-split")
	r.NoError(err)
	r.Contains(row.Config, "env")
	r.NotContains(row.Config, "secrets")

	blob, err := json.Marshal(row.Config)
	r.NoError(err)
	r.NotContains(string(blob), jsSecretNeedle, "the fixture secret must not be in the public config")

	// The runtime still sees both.
	output := runJS(t, rig.effectiveConfig(t, "js-split"))
	r.Equal(jsSecretNeedle, output["secret"])
	r.Equal("https://acme.com", output["base"])
}

// TestJSExportRedactsSecretsKeepsEnv is the export-side half.
func TestJSExportRedactsSecretsKeepsEnv(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	rig := newJSRig(t)

	rig.createJSCheck(t, "js-export")

	doc, err := rig.svc.ExportChecks(t.Context(), rig.org.Slug, checks.ListChecksOptions{})
	r.NoError(err)

	exported := findExportedCheck(t, doc, "js-export")
	r.NotContains(exported.Config, "secrets", "export must strip the secrets map")

	env, ok := exported.Config["env"].(map[string]any)
	r.True(ok, "export must keep the env map, got %#v", exported.Config["env"])
	r.Equal("https://acme.com", env["BASE_URL"])

	rendered, err := json.Marshal(doc)
	r.NoError(err)
	r.NotContains(string(rendered), jsSecretNeedle)
}

// TestJSImportOmittingSecretsPreservesThem is the write-side half, and the one
// that catches the "save wipes the secret" bug class: a document that carries
// no `secrets` key must leave the stored one alone while an `env` change lands.
func TestJSImportOmittingSecretsPreservesThem(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	rig := newJSRig(t)
	ctx := t.Context()

	rig.createJSCheck(t, "js-import")

	doc, err := rig.svc.ExportChecks(ctx, rig.org.Slug, checks.ListChecksOptions{})
	r.NoError(err)

	// Change env, leave the (already stripped) secrets key absent.
	for i := range doc.Checks {
		if doc.Checks[i].Slug == "js-import" {
			doc.Checks[i].Config["env"] = map[string]any{"BASE_URL": "https://acme.dev"}
		}
	}

	res, err := rig.svc.ImportChecks(ctx, rig.org.Slug, doc, false)
	r.NoError(err)
	r.Empty(res.Errors, "import reported errors: %+v", res)
	r.Equal(1, res.Updated, "the env change must land as an update: %+v", res)

	effective := rig.effectiveConfig(t, "js-import")

	output := runJS(t, effective)
	r.Equal(jsSecretNeedle, output["secret"],
		"an import that omits `secrets` must preserve the stored value")
	r.Equal("https://acme.dev", output["base"], "the env change must still apply")
}

// TestJSSecretsAcceptSecretReferences pins that the `${param:}` grammar of spec
// 2026-09-11-03 reaches INSIDE the secrets map, with no second resolution path:
// the reference is what is stored (even in the encrypted envelope), and the
// dispatch-boundary overlay is what materializes it.
func TestJSSecretsAcceptSecretReferences(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	rig := newJSRig(t)
	ctx := t.Context()

	r.NoError(rig.dbSvc.SetOrgParameter(ctx, rig.org.UID,
		paramkeys.StorageKey("sso-password"), jsSecretNeedle, true))

	period := "5m"
	_, err := rig.svc.CreateCheck(ctx, rig.org.Slug, checks.CreateCheckRequest{
		Name: "JS ref", Slug: "js-ref", Type: "js",
		Config: map[string]any{
			"script":  jsScript,
			"env":     map[string]any{"BASE_URL": "https://acme.com"},
			"secrets": map[string]any{"PASSWORD": "${param:sso-password}"},
		},
		Period: &period,
	})
	r.NoError(err)

	// At rest — including inside the envelope — it is still the reference.
	effective := rig.effectiveConfig(t, "js-ref")
	stored, ok := effective["secrets"].(map[string]any)
	r.True(ok)
	r.Equal("${param:sso-password}", stored["PASSWORD"],
		"a reference must be STORED, never resolved at write time")

	// At dispatch, the overlay resolves it — and the script reads the value.
	overlay, err := checkjobsvc.ParamOverlay(ctx, rig.dbSvc, rig.org.UID, effective)
	r.NoError(err)
	r.Contains(overlay, "secrets", "the overlay must cover references nested in the secrets map")

	merged := credentials.MergeConfig(effective, overlay)

	output := runJS(t, merged)
	r.Equal(jsSecretNeedle, output["secret"])
	r.Equal("https://acme.com", output["base"])
}

// TestJSSecretsAcceptEnvReferences is the `${env:}` half of the reference
// grammar: unlike `${param:}` it is deliberately NOT resolved by the API (so a
// deported agent resolves it against ITS own environment), which means the
// stored envelope and the dispatch overlay both still carry the reference and
// only the executing process materializes it.
//
// Uses t.Setenv, which is incompatible with t.Parallel.
func TestJSSecretsAcceptEnvReferences(t *testing.T) {
	r := require.New(t)
	rig := newJSRig(t)
	ctx := t.Context()

	t.Setenv("SP_TEST_JS_SECRET", jsEnvRefNeedle)

	period := "5m"
	_, err := rig.svc.CreateCheck(ctx, rig.org.Slug, checks.CreateCheckRequest{
		Name: "JS env ref", Slug: "js-env-ref", Type: "js",
		Config: map[string]any{
			"script":  jsScript,
			"env":     map[string]any{"BASE_URL": "https://acme.com"},
			"secrets": map[string]any{"PASSWORD": "${env:SP_TEST_JS_SECRET}"},
		},
		Period: &period,
	})
	r.NoError(err)

	effective := rig.effectiveConfig(t, "js-env-ref")
	stored, ok := effective["secrets"].(map[string]any)
	r.True(ok)
	r.Equal("${env:SP_TEST_JS_SECRET}", stored["PASSWORD"])

	// The API resolver must leave it alone — that is the per-region-secret
	// feature, not an oversight.
	overlay, err := checkjobsvc.ParamOverlay(ctx, rig.dbSvc, rig.org.UID, effective)
	r.NoError(err)
	r.NotContains(overlay, "secrets", "${env:} must NOT be resolved on the API side")

	// The executing process is what materializes it.
	resolved, _, err := secretref.ResolveConfig(ctx, effective, secretref.ExecutionResolver())
	r.NoError(err)

	output := runJS(t, resolved)
	r.Equal(jsEnvRefNeedle, output["secret"])
	r.Equal("https://acme.com", output["base"])
}

// findExportedCheck returns the exported entry for a slug.
func findExportedCheck(t *testing.T, doc *checks.ExportDocument, slug string) checks.ExportCheck {
	t.Helper()

	for _, c := range doc.Checks {
		if c.Slug == slug {
			return c
		}
	}

	t.Fatalf("check %q missing from the export", slug)

	return checks.ExportCheck{}
}
