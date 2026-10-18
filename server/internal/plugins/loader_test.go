package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
	"github.com/fclairamb/solidping/server/internal/checkers/registrycore"
	"github.com/fclairamb/solidping/server/internal/checkers/schemas"
)

//nolint:gochecknoglobals // unique type names across parallel tests
var typeSeq atomic.Int64

// uniqueType returns a plugin type name no other test uses.
func uniqueType(prefix string) string {
	return fmt.Sprintf("x-%s-%d", prefix, typeSeq.Add(1))
}

const testSchema = `{
  "type": "object",
  "properties": {
    "host": {"type": "string", "minLength": 1},
    "token": {"type": "string"},
    "timeout": {"type": "string"}
  },
  "required": ["host"],
  "additionalProperties": false,
  "x-solidping-secret-fields": ["token"]
}`

const testScript = `return { status: "up", output: { message: "host=" + config.host } };`

// writePlugin writes a plugin folder. files maps a relative path to content;
// the manifest is marshaled from manifest.
func writePlugin(t *testing.T, root, name string, manifest map[string]any, files map[string]string) {
	t.Helper()

	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o750))

	raw, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ManifestFile), raw, 0o600))

	for rel, content := range files {
		path := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}
}

func jsManifest(name string, types ...map[string]any) map[string]any {
	decls := make([]any, 0, len(types))
	for _, decl := range types {
		decls = append(decls, decl)
	}

	return map[string]any{"name": name, "version": "1.0.0", "protocol": "js", "entry": "check.js", "types": decls}
}

func typeDecl(checkType string) map[string]any {
	return map[string]any{
		"type": checkType, "description": "test type", "labels": []string{"safe"}, "schema": "schema.json",
	}
}

func loadForTest(t *testing.T, root string) *Set {
	t.Helper()

	set := Load(t.Context(), root, nil)
	t.Cleanup(set.Close)

	return set
}

func TestLoadValidPlugin(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	root := t.TempDir()
	checkType := uniqueType("valid")

	decl := typeDecl(checkType)
	decl["minPeriod"] = "1m"
	decl["defaultPeriod"] = "5m"
	writePlugin(t, root, "valid", jsManifest("valid", decl),
		map[string]string{"check.js": testScript, "schema.json": testSchema})

	set := loadForTest(t, root)
	r.Empty(set.Errors)
	r.Equal([]checkerdef.CheckType{checkerdef.CheckType(checkType)}, set.Types)

	meta := checkerdef.GetCheckTypeMeta(checkerdef.CheckType(checkType))
	r.NotNil(meta)
	r.True(meta.ExcludedFromTally)
	r.Equal(&checkerdef.PluginRef{Name: "valid", Version: "1.0.0"}, meta.Plugin)
	r.Equal([]string{"safe", "category:other"}, meta.Labels)
	r.Equal("1m0s", meta.MinPeriod.String())
	r.Equal("5m0s", meta.DefaultPeriod.String())

	r.Contains(registrycore.PluginTypes(), checkType)
	r.True(configregistry.IsKnownType(checkerdef.CheckType(checkType)))

	raw, err := schemas.Get(checkType)
	r.NoError(err)
	r.Contains(string(raw), `"x-solidping-plugin"`)
	r.Contains(schemas.Types(), checkType)

	checker, ok := registrycore.GetChecker(checkerdef.CheckType(checkType))
	r.True(ok)

	cfg, ok := registrycore.ParseConfig(checkerdef.CheckType(checkType))
	r.True(ok)
	r.NoError(cfg.FromMap(map[string]any{"host": "gopher.acme.com"}))

	res, err := checker.Execute(t.Context(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, res.Status, res.Output)
	r.Equal("host=gopher.acme.com", res.Output["message"])

	// Close unregisters everything.
	set.Close()
	r.Nil(checkerdef.GetCheckTypeMeta(checkerdef.CheckType(checkType)))
	_, ok = registrycore.GetChecker(checkerdef.CheckType(checkType))
	r.False(ok)
	_, err = schemas.Get(checkType)
	r.ErrorIs(err, schemas.ErrNotFound)
}

func TestLoadSkipsBrokenPlugins(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	root := t.TempDir()
	good := uniqueType("good")
	dupe := uniqueType("dupe")
	files := map[string]string{"check.js": testScript, "schema.json": testSchema}

	writePlugin(t, root, "a-good", jsManifest("good", typeDecl(good)), files)
	writePlugin(t, root, "b-no-prefix", jsManifest("noprefix", typeDecl("gopher")), files)
	writePlugin(t, root, "c-dupe", jsManifest("dupe", typeDecl(dupe), typeDecl(dupe)), files)
	writePlugin(t, root, "d-broken-schema", jsManifest("broken", typeDecl(uniqueType("broken"))),
		map[string]string{"check.js": testScript, "schema.json": `{"type": "nope"}`})
	writePlugin(t, root, "e-missing-entry", jsManifest("missing", typeDecl(uniqueType("missing"))),
		map[string]string{"schema.json": testSchema})

	escaping := jsManifest("escape", typeDecl(uniqueType("escape")))
	escaping["entry"] = "../a-good/check.js"
	writePlugin(t, root, "f-escape", escaping, map[string]string{"schema.json": testSchema})

	require.NoError(t, os.MkdirAll(filepath.Join(root, "g-no-manifest"), 0o750))

	shadow := jsManifest("shadow", typeDecl(good))
	writePlugin(t, root, "h-shadow", shadow, files)

	set := loadForTest(t, root)

	// The good plugin and the first declaration of the duplicate still load.
	r.ElementsMatch([]checkerdef.CheckType{checkerdef.CheckType(good), checkerdef.CheckType(dupe)}, set.Types)

	wantErrs := []error{
		ErrTypePrefix, ErrDuplicateType, ErrBadSchema, ErrEntryMissing, ErrEntryOutside, ErrInvalidManifest,
	}
	for _, want := range wantErrs {
		found := false

		for _, got := range set.Errors {
			if errors.Is(got, want) {
				found = true
			}
		}

		r.True(found, "expected a %v among %v", want, set.Errors)
	}

	// The shadowing plugin (same type as a-good) is refused as a duplicate.
	shadowRefused := false

	for _, got := range set.Errors {
		if errors.Is(got, ErrDuplicateType) && strings.Contains(got.Error(), "plugin shadow") {
			shadowRefused = true
		}
	}

	r.True(shadowRefused, set.Errors)
}

func TestExecPluginsAreAlwaysUnsafe(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal([]string{"unsafe", "category:network"}, pluginLabels(ProtocolExec, []string{"safe", "category:network"}))
	r.Equal([]string{"unsafe", "category:other"}, pluginLabels(ProtocolNagios, nil))
	r.Equal([]string{"safe", "category:other"}, pluginLabels(ProtocolJS, nil))
	r.Equal([]string{"unsafe", "category:other"}, pluginLabels(ProtocolJS, []string{"unsafe"}))
}

func TestLoadEmptyDirIsOff(t *testing.T) {
	t.Parallel()

	set := Load(t.Context(), "", nil)
	require.Empty(t, set.Types)
	require.Empty(t, set.Errors)
}
