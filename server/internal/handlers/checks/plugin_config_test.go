package checks_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/plugins"
)

// pluginSecretNeedle is the plugin fixture's secret value.
const pluginSecretNeedle = "plugin-fixture-token"

// loadTestPlugin registers a js plugin type whose schema declares `token` as a
// secret field (spec 2026-10-17-05 §6).
func loadTestPlugin(t *testing.T, checkType string) {
	t.Helper()
	r := require.New(t)

	dir := filepath.Join(t.TempDir(), "plugin")
	r.NoError(os.MkdirAll(dir, 0o750))

	manifest := map[string]any{
		"name": "secrets", "version": "0.1.0", "protocol": "js", "entry": "check.js",
		"types": []any{map[string]any{
			"type": checkType, "description": "test", "labels": []string{"safe"}, "schema": "schema.json",
		}},
	}

	raw, err := json.Marshal(manifest)
	r.NoError(err)
	r.NoError(os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o600))
	r.NoError(os.WriteFile(filepath.Join(dir, "check.js"),
		[]byte(`return { status: "up", output: { message: config.host } };`), 0o600))
	r.NoError(os.WriteFile(filepath.Join(dir, "schema.json"), []byte(`{
  "type": "object",
  "properties": {"host": {"type": "string", "minLength": 1}, "token": {"type": "string"}},
  "required": ["host"],
  "additionalProperties": false,
  "x-solidping-secret-fields": ["token"]
}`), 0o600))

	set := plugins.Load(filepath.Dir(dir), nil)
	t.Cleanup(set.Close)
	r.Empty(set.Errors)
	r.Equal([]checkerdef.CheckType{checkerdef.CheckType(checkType)}, set.Types)
}

func TestPluginConfigSchemaValidationAndSecrets(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	const checkType = "x-checks-secret"

	loadTestPlugin(t, checkType)

	rig := newJSRig(t)
	period := "5m"

	// Schema-invalid config: a ConfigError, which the handler maps to 400
	// VALIDATION_ERROR. Nothing is stored.
	_, err := rig.svc.CreateCheck(ctx, rig.org.Slug, checks.CreateCheckRequest{
		Name: "Bad", Slug: "plugin-bad", Type: checkType,
		Config: map[string]any{"token": pluginSecretNeedle}, Period: &period,
	})
	r.Error(err)

	configErr := checkerdef.IsConfigError(err)
	r.NotNil(configErr, "got %v", err)
	r.Equal("host", configErr.Parameter)

	_, err = rig.dbSvc.GetCheckByUidOrSlug(ctx, rig.org.UID, "plugin-bad")
	r.Error(err, "an invalid plugin config must not be stored")

	// Schema-valid config is accepted.
	_, err = rig.svc.CreateCheck(ctx, rig.org.Slug, checks.CreateCheckRequest{
		Name: "Good", Slug: "plugin-good", Type: checkType,
		Config: map[string]any{"host": "gopher.acme.com", "token": pluginSecretNeedle}, Period: &period,
	})
	r.NoError(err)

	// At rest: host is public, token is in the encrypted envelope.
	row, err := rig.dbSvc.GetCheckByUidOrSlug(ctx, rig.org.UID, "plugin-good")
	r.NoError(err)
	r.Equal("gopher.acme.com", row.Config["host"])
	r.NotContains(row.Config, "token")
	r.NotNil(row.ConfigPrivate)

	blob, err := json.Marshal(row.Config)
	r.NoError(err)
	r.NotContains(string(blob), pluginSecretNeedle)

	effective := rig.effectiveConfig(t, "plugin-good")
	r.Equal(pluginSecretNeedle, effective["token"], "the worker still gets the secret")

	// Export: the secret is stripped, the public field kept.
	doc, err := rig.svc.ExportChecks(ctx, rig.org.Slug, checks.ListChecksOptions{})
	r.NoError(err)

	exported := findExportedCheck(t, doc, "plugin-good")
	r.Equal("gopher.acme.com", exported.Config["host"])
	r.NotContains(exported.Config, "token")

	rendered, err := json.Marshal(doc)
	r.NoError(err)
	r.NotContains(string(rendered), pluginSecretNeedle)

	// PATCH validates against the schema too.
	_, err = rig.svc.UpdateCheck(ctx, rig.org.Slug, "plugin-good", &checks.UpdateCheckRequest{
		Config: &map[string]any{"host": ""},
	})
	r.Error(err)
	r.NotNil(checkerdef.IsConfigError(err), "got %v", err)
}
