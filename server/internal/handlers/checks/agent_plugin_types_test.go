package checks_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/plugins/protocol"
	"github.com/fclairamb/solidping/server/internal/plugintypes"
)

// A plugin type an org's agent reported is creatable, validated against the
// reported schema and has its secret fields encrypted, in that org only
// (spec 2026-10-17-05 §18). The server never loaded the plugin itself.
func TestAgentReportedPluginTypeIsScopedToItsOrg(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	const checkType = "x-agent-only"

	rig := newJSRig(t)
	period := "5m"

	other := models.NewOrganization("other-org", "Other Org")
	r.NoError(rig.dbSvc.CreateOrganization(ctx, other))

	request := func(slug string, config map[string]any) checks.CreateCheckRequest {
		return checks.CreateCheckRequest{Name: slug, Slug: slug, Type: checkType, Config: config, Period: &period}
	}

	// Before any report the type is unknown.
	_, err := rig.svc.CreateCheck(ctx, rig.org.Slug, request("before", map[string]any{"host": "gopher.acme.com"}))
	r.ErrorIs(err, checks.ErrInvalidCheckType)

	stored, err := plugintypes.New(rig.dbSvc.DB()).Report(ctx, rig.org.UID, []protocol.TypeManifest{{
		Type: checkType, Plugin: "acme", Version: "1.0.0", Protocol: "exec",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {"host": {"type": "string", "minLength": 1}, "token": {"type": "string"}},
  "required": ["host"],
  "x-solidping-secret-fields": ["token"]
}`),
	}})
	r.NoError(err)
	r.Equal(1, stored)

	// Schema-invalid: VALIDATION_ERROR on host, nothing stored.
	_, err = rig.svc.CreateCheck(ctx, rig.org.Slug, request("bad", map[string]any{"token": pluginSecretNeedle}))
	configErr := checkerdef.IsConfigError(err)
	r.NotNil(configErr, "got %v", err)
	r.Equal("host", configErr.Parameter)

	// Valid: created, token encrypted at rest.
	_, err = rig.svc.CreateCheck(ctx, rig.org.Slug,
		request("good", map[string]any{"host": "gopher.acme.com", "token": pluginSecretNeedle}))
	r.NoError(err)

	row, err := rig.dbSvc.GetCheckByUidOrSlug(ctx, rig.org.UID, "good")
	r.NoError(err)
	r.Equal("gopher.acme.com", row.Config["host"])
	r.NotContains(row.Config, "token")
	r.NotNil(row.ConfigPrivate)

	// The dry-run validate endpoint knows the type too.
	validated, err := rig.svc.ValidateCheck(ctx, rig.org.Slug, &checks.ValidateCheckRequest{
		Type: checkType, Config: map[string]any{"host": ""},
	})
	r.NoError(err)
	r.False(validated.Valid)

	// PATCH validates against the reported schema.
	_, err = rig.svc.UpdateCheck(ctx, rig.org.Slug, "good", &checks.UpdateCheckRequest{
		Config: &map[string]any{"host": ""},
	})
	r.NotNil(checkerdef.IsConfigError(err), "got %v", err)

	// Another org never resolves it.
	_, err = rig.svc.CreateCheck(ctx, other.Slug, request("leak", map[string]any{"host": "gopher.acme.com"}))
	r.ErrorIs(err, checks.ErrInvalidCheckType)

	validated, err = rig.svc.ValidateCheck(ctx, other.Slug, &checks.ValidateCheckRequest{
		Type: checkType, Config: map[string]any{"host": "gopher.acme.com"},
	})
	r.NoError(err)
	r.False(validated.Valid)
	r.Equal(checks.CodeUnsupportedType, validated.Fields[0].Code)
}

// agentTypeSchema is the reported schema: host required, token a secret.
const agentTypeSchema = `{
  "type": "object",
  "properties": {"host": {"type": "string", "minLength": 1}, "token": {"type": "string"}},
  "required": ["host"],
  "x-solidping-secret-fields": ["token"]
}`

// reportAgentType stores checkType as reported by one of orgUID's agents.
func reportAgentType(t *testing.T, db bun.IDB, orgUID, checkType string, edit func(*protocol.TypeManifest)) {
	t.Helper()

	manifest := protocol.TypeManifest{
		Type: checkType, Plugin: "acme", Version: "1.0.0", Protocol: "exec",
		Schema: json.RawMessage(agentTypeSchema),
	}
	if edit != nil {
		edit(&manifest)
	}

	stored, err := plugintypes.New(db).Report(t.Context(), orgUID, []protocol.TypeManifest{manifest})
	require.NoError(t, err)
	require.Equal(t, 1, stored)
}

// A PATCH that does not mention an agent type's secret keeps it, like any
// other type's (spec 2026-10-17-05 §18).
func TestAgentPluginTypePatchKeepsTheSecret(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	const checkType = "x-agent-patch"

	rig := newJSRig(t)
	period := "5m"

	reportAgentType(t, rig.dbSvc.DB(), rig.org.UID, checkType, nil)

	_, err := rig.svc.CreateCheck(ctx, rig.org.Slug, checks.CreateCheckRequest{
		Name: "Patched", Slug: "patched", Type: checkType, Period: &period,
		Config: map[string]any{"host": "gopher.acme.com", "token": pluginSecretNeedle},
	})
	r.NoError(err)

	_, err = rig.svc.UpdateCheck(ctx, rig.org.Slug, "patched", &checks.UpdateCheckRequest{
		Config: &map[string]any{"host": "other.acme.com"},
	})
	r.NoError(err)

	effective := rig.effectiveConfig(t, "patched")
	r.Equal("other.acme.com", effective["host"], "positive control: the patch landed")
	r.Equal(pluginSecretNeedle, effective["token"])
}

// The min/max period the agent reported bounds the period on every write
// (spec 2026-10-17-05 §18).
func TestAgentPluginTypePeriodBounds(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	const checkType = "x-agent-period"

	rig := newJSRig(t)

	reportAgentType(t, rig.dbSvc.DB(), rig.org.UID, checkType, func(manifest *protocol.TypeManifest) {
		manifest.MinPeriod = "1m"
		manifest.MaxPeriod = "1h"
	})

	create := func(slug, period string) error {
		_, err := rig.svc.CreateCheck(ctx, rig.org.Slug, checks.CreateCheckRequest{
			Name: slug, Slug: slug, Type: checkType, Period: &period,
			Config: map[string]any{"host": "gopher.acme.com"},
		})

		return err
	}

	r.Error(create("too-fast", "30s"))
	r.Error(create("too-slow", "2h"))
	r.NoError(create("in-bounds", "5m"), "positive control")

	tooFast := "30s"
	_, err := rig.svc.UpdateCheck(ctx, rig.org.Slug, "in-bounds", &checks.UpdateCheckRequest{Period: &tooFast})
	r.Error(err)

	validated, err := rig.svc.ValidateCheck(ctx, rig.org.Slug, &checks.ValidateCheckRequest{
		Type: checkType, Config: map[string]any{"host": "gopher.acme.com"}, Period: "2h",
	})
	r.NoError(err)
	r.False(validated.Valid)

	// Import holds a document to the same bounds.
	res, err := rig.svc.ImportChecks(ctx, rig.org.Slug, &checks.ExportDocument{
		Version: 1, Organization: rig.org.Slug, Checks: []checks.ExportCheck{{
			Name: "doc", Slug: "doc-too-fast", Type: checkType, Period: "30s", Enabled: true,
			Config: map[string]any{"host": "gopher.acme.com"},
		}},
	}, true)
	r.NoError(err)
	r.Len(res.Errors, 1)
	r.Contains(res.Errors[0].Error, "period")
}

// Config-as-code resolves the org's agent types: the org-scoped validator
// and the import accept them, the offline validator does not know them.
func TestAgentPluginTypeInConfigAsCode(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	const checkType = "x-agent-doc"

	rig := newJSRig(t)

	reportAgentType(t, rig.dbSvc.DB(), rig.org.UID, checkType, nil)

	document := func(config map[string]any) *checks.ExportDocument {
		return &checks.ExportDocument{
			Version: 1, Organization: rig.org.Slug, Checks: []checks.ExportCheck{{
				Name: "doc", Slug: "doc-check", Type: checkType, Period: "5m", Enabled: true, Config: config,
			}},
		}
	}

	validated, err := rig.svc.ValidateDocumentForOrg(ctx, rig.org.Slug,
		document(map[string]any{"host": "gopher.acme.com"}), false)
	r.NoError(err)
	r.True(validated.Valid, "%v", validated.Issues)

	// The reported schema applies.
	validated, err = rig.svc.ValidateDocumentForOrg(ctx, rig.org.Slug, document(map[string]any{}), false)
	r.NoError(err)
	r.False(validated.Valid)

	// The declared secret is flagged when inlined.
	validated, err = rig.svc.ValidateDocumentForOrg(ctx, rig.org.Slug,
		document(map[string]any{"host": "gopher.acme.com", "token": pluginSecretNeedle}), false)
	r.NoError(err)
	r.False(validated.Valid)
	r.Equal(checks.CodeInlinedCredential, validated.Issues[0].Code)

	// Offline nothing is known about the org.
	r.NotEmpty(checks.ValidateDocument(document(map[string]any{"host": "gopher.acme.com"})))

	res, err := rig.svc.ImportChecks(ctx, rig.org.Slug, document(map[string]any{"host": "gopher.acme.com"}), false)
	r.NoError(err)
	r.Empty(res.Errors)
	r.Equal(1, res.Created)
}
