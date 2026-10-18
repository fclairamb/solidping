package plugins

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
)

// An exec plugin type is unsafe whatever its manifest says, and an unsafe
// plugin type is off by default on a SaaS deployment (spec 2026-10-17-05 §19).
func TestUnsafePluginTypesAreOffInSaaS(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	root := t.TempDir()
	checkType := uniqueType("exec")

	manifest := jsManifest("execy", typeDecl(checkType))
	manifest["protocol"] = ProtocolExec
	manifest["entry"] = "plugin"
	writePlugin(t, root, "execy", manifest, map[string]string{"plugin": "not run", "schema.json": testSchema})

	set := loadForTest(t, root)
	r.Empty(set.Errors)

	ct := checkerdef.CheckType(checkType)
	r.Contains(checkerdef.GetCheckTypeMeta(ct).Labels, "unsafe")

	selfHosted := checkerdef.NewActivationResolver(&config.CheckersConfig{}, config.DeploymentModeSelfHosted)
	r.True(selfHosted.IsTypeEnabled(ct, nil), "positive control: enabled when self-hosted")

	saas := checkerdef.NewActivationResolver(&config.CheckersConfig{}, config.DeploymentModeSaaS)
	r.False(saas.IsTypeEnabled(ct, nil))
	r.True(saas.IsTypeEnabled(checkerdef.CheckTypeHTTP, nil))

	optedIn := checkerdef.NewActivationResolver(&config.CheckersConfig{Enabled: []string{checkType}},
		config.DeploymentModeSaaS)
	r.True(optedIn.IsTypeEnabled(ct, nil))
}
