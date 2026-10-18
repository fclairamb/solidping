package plugins

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/plugins/protocol"
)

func reportedType(checkType string) *protocol.TypeManifest {
	for _, manifest := range LoadedTypeManifests() {
		if manifest.Type == checkType {
			return &manifest
		}
	}

	return nil
}

// What a loaded plugin reports to the server is its manifest plus the raw
// schema file (spec 2026-10-17-05 §18).
func TestLoadedTypeManifestsReportTheGopherPlugin(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	loadGopher(t)

	gopher := reportedType(string(gopherType))
	r.NotNil(gopher)
	r.Equal("gopher", gopher.Plugin)
	r.Equal("1.0.0", gopher.Version)
	r.Equal(ProtocolJS, gopher.Protocol)
	r.Contains(string(gopher.Schema), `"host"`)

	// What the server compiles from it validates like the loaded type.
	remote, err := CompileRemote(gopher)
	r.NoError(err)
	r.Equal(gopherType, remote.Meta().Type)
	r.True(remote.Meta().ExcludedFromTally)
	r.Error(remote.ValidateSpec(&checkerdef.CheckSpec{Config: map[string]any{"port": 70}}), "host is required")
	r.NoError(remote.ValidateSpec(&checkerdef.CheckSpec{Config: map[string]any{"host": "gopher.acme.com"}}))
}

// A closed set reports nothing any more.
func TestClosedSetIsNotReported(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	root := t.TempDir()
	checkType := uniqueType("reported")
	writePlugin(t, root, "reported", jsManifest("reported", typeDecl(checkType)),
		map[string]string{"check.js": testScript, "schema.json": testSchema})

	set := Load(root, nil)
	r.Empty(set.Errors)

	reported := reportedType(checkType)
	r.NotNil(reported)
	r.JSONEq(testSchema, string(reported.Schema))

	set.Close()
	r.Nil(reportedType(checkType))
}

func TestCompileRemoteRejectsBadReports(t *testing.T) {
	t.Parallel()

	valid := func() protocol.TypeManifest {
		return protocol.TypeManifest{
			Type: "x-remote", Plugin: "acme", Version: "1.0.0", Protocol: ProtocolJS,
			Schema: json.RawMessage(`{"type":"object"}`),
		}
	}

	_, err := CompileRemote(new(valid()))
	require.NoError(t, err, "positive control")

	tests := []struct {
		name   string
		mutate func(*protocol.TypeManifest)
		want   error
	}{
		{"no x- prefix", func(m *protocol.TypeManifest) { m.Type = "remote" }, ErrTypePrefix},
		{"unknown protocol", func(m *protocol.TypeManifest) { m.Protocol = "so" }, ErrInvalidManifest},
		{"no schema", func(m *protocol.TypeManifest) { m.Schema = nil }, ErrInvalidManifest},
		{"schema too large", func(m *protocol.TypeManifest) {
			m.Schema = json.RawMessage(`{"description":"` + strings.Repeat("a", maxRemoteSchemaBytes) + `"}`)
		}, ErrTooLarge},
		{"broken schema", func(m *protocol.TypeManifest) { m.Schema = json.RawMessage(`{"type":42}`) }, ErrBadSchema},
		{"no plugin name", func(m *protocol.TypeManifest) { m.Plugin = "" }, ErrInvalidManifest},
		{"description too long", func(m *protocol.TypeManifest) {
			m.Description = strings.Repeat("a", maxRemoteTextLen+1)
		}, ErrInvalidManifest},
		{"too many labels", func(m *protocol.TypeManifest) {
			m.Labels = make([]string, maxRemoteLabels+1)
		}, ErrInvalidManifest},
		{"bad period", func(m *protocol.TypeManifest) { m.MinPeriod = "soon" }, ErrInvalidManifest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			manifest := valid()
			tc.mutate(&manifest)

			_, err := CompileRemote(&manifest)
			require.ErrorIs(t, err, tc.want)
		})
	}
}

// An exec or nagios type an agent reports is unsafe whatever it declares.
func TestCompileRemoteForcesUnsafeOutsideJS(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	remote, err := CompileRemote(&protocol.TypeManifest{
		Type: "x-remote-nagios", Plugin: "acme", Version: "1.0.0", Protocol: ProtocolNagios,
		Labels: []string{"safe"}, Schema: json.RawMessage(`{"type":"object"}`),
	})
	r.NoError(err)
	r.Contains(remote.Meta().Labels, "unsafe")
	r.NotContains(remote.Meta().Labels, "safe")
}
