package backend

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/plugins"
	"github.com/fclairamb/solidping/server/internal/plugins/protocol"
)

// The report stays under the server's frame cap and the type count bound:
// a frame over the cap would drop the agent's connection.
func TestFitPluginReportBoundsTheFrame(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	big := json.RawMessage(`{"description":"` + strings.Repeat("a", 60*1024) + `"}`)
	manifests := make([]protocol.TypeManifest, 0, 10)

	for i := range 10 {
		manifests = append(manifests, protocol.TypeManifest{Type: fmt.Sprintf("x-big-%d", i), Schema: big})
	}

	fitted := fitPluginReport(manifests)
	r.NotEmpty(fitted)
	r.Less(len(fitted), len(manifests))

	raw, err := json.Marshal(fitted)
	r.NoError(err)
	r.LessOrEqual(len(raw), maxFrameBytes*3/4+len(fitted))

	small := make([]protocol.TypeManifest, 0, plugins.MaxReportedTypes+5)
	for i := range plugins.MaxReportedTypes + 5 {
		small = append(small, protocol.TypeManifest{Type: fmt.Sprintf("x-small-%d", i), Schema: json.RawMessage(`{}`)})
	}

	r.Len(fitPluginReport(small), plugins.MaxReportedTypes)
}

// The manifests ride the first claim of a connection only.
//
//nolint:paralleltest // loads a plugin into the process-wide registry
func TestPluginReportOncePerConnection(t *testing.T) {
	r := require.New(t)

	dir := filepath.Join(t.TempDir(), "report")
	r.NoError(os.MkdirAll(dir, 0o750))
	r.NoError(os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{
  "name": "report", "version": "1.0.0", "protocol": "js", "entry": "check.js",
  "types": [{"type": "x-backend-report", "description": "t", "labels": ["safe"], "schema": "schema.json"}]
}`), 0o600))
	r.NoError(os.WriteFile(filepath.Join(dir, "check.js"), []byte(`return {status: "up"};`), 0o600))
	r.NoError(os.WriteFile(filepath.Join(dir, "schema.json"), []byte(`{"type":"object"}`), 0o600))

	set := plugins.Load(filepath.Dir(dir), nil)
	t.Cleanup(set.Close)
	r.Empty(set.Errors)

	backend := &WSBackend{connectedAt: time.Now()}

	manifests, reportFor := backend.pluginReport()
	r.NotEmpty(manifests)
	r.Equal(backend.connectedAt, reportFor.connectedAt)

	// claim marks the connection as reported after a successful request.
	backend.pluginsReportedFor = reportFor

	manifests, _ = backend.pluginReport()
	r.Nil(manifests, "the same connection does not get them twice")

	backend.connectedAt = time.Now().Add(time.Second)

	manifests, reportFor = backend.pluginReport()
	r.NotEmpty(manifests, "a new connection gets them again")

	backend.pluginsReportedFor = reportFor

	// A change in the loaded types (an http plugin refresh) reports again on
	// the same connection.
	other := filepath.Join(t.TempDir(), "other")
	r.NoError(os.MkdirAll(other, 0o750))
	r.NoError(os.WriteFile(filepath.Join(other, "manifest.json"), []byte(`{
  "name": "report2", "version": "1.0.0", "protocol": "js", "entry": "check.js",
  "types": [{"type": "x-backend-report-two", "description": "t", "labels": ["safe"], "schema": "schema.json"}]
}`), 0o600))
	r.NoError(os.WriteFile(filepath.Join(other, "check.js"), []byte(`return {status: "up"};`), 0o600))
	r.NoError(os.WriteFile(filepath.Join(other, "schema.json"), []byte(`{"type":"object"}`), 0o600))

	otherSet := plugins.Load(filepath.Dir(other), nil)
	t.Cleanup(otherSet.Close)
	r.Empty(otherSet.Errors)

	manifests, _ = backend.pluginReport()
	r.Len(manifests, 2, "the types changed: reported again on the same connection")
}
