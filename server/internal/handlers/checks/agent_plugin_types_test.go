package checks_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

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
