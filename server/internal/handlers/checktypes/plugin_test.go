package checktypes

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/plugins"
)

// registryMu serializes tests that load plugins (writers) against tests whose
// assertions need the set of registered types to stay put (readers).
var registryMu sync.RWMutex //nolint:gochecknoglobals // test-wide lock around a process-wide registry

// The schema endpoint and the check-types listing serve a plugin type with
// its schema and the plugin's identity (spec 2026-10-17-05 §8).
//
// plugins.Load registers x-gopher in a process-wide registry and Close removes
// it again, so the test holds registryMu for writing while it is loaded.
func TestPluginTypeIsServed(t *testing.T) {
	t.Parallel()
	registryMu.Lock()
	defer registryMu.Unlock()

	r := require.New(t)

	set := plugins.Load(t.Context(), "../../../../plugins", nil)
	// Not t.Cleanup: that would run after the deferred unlock above.
	defer set.Close()
	r.Empty(set.Errors)

	rec := get(t, schemaRouter(t), "/api/v1/checks/schema/x-gopher")
	r.Equal(http.StatusOK, rec.Code)
	r.Equal("application/schema+json", rec.Header().Get("Content-Type"))

	var doc struct {
		Plugin     checkerdef.PluginRef `json:"x-solidping-plugin"` //nolint:tagliatelle // JSON Schema extension keyword
		Required   []string             `json:"required"`
		Properties map[string]any       `json:"properties"`
	}

	r.NoError(json.Unmarshal(rec.Body.Bytes(), &doc))
	r.Equal(checkerdef.PluginRef{Name: "gopher", Version: "1.0.0"}, doc.Plugin)
	r.Equal([]string{"host"}, doc.Required)
	r.Contains(doc.Properties, "selector")

	catalog := get(t, schemaRouter(t), "/api/v1/checks/schema")
	r.Contains(catalog.Body.String(), `"/api/v1/checks/schema/x-gopher"`)

	resolver := checkerdef.NewActivationResolver(&config.CheckersConfig{}, config.DeploymentModeSelfHosted)
	listing := NewService(resolver, "").ListServerCheckTypes()

	var gopher *CheckTypeResponse

	for i := range listing.Data {
		if listing.Data[i].Type == "x-gopher" {
			gopher = &listing.Data[i]
		}
	}

	r.NotNil(gopher, "x-gopher must be listed")
	r.Equal(&checkerdef.PluginRef{Name: "gopher", Version: "1.0.0"}, gopher.Plugin)
	r.True(gopher.Enabled)
	r.True(gopher.ExcludedFromTally)
	r.Equal(60, gopher.MinPeriodSeconds)
	r.Equal(300, gopher.DefaultPeriodSeconds)
	r.Equal([]string{}, gopher.SecretFields)

	for i := range listing.Data {
		if listing.Data[i].Type == "http" {
			r.Nil(listing.Data[i].Plugin, "built-ins carry no plugin")
		}
	}
}
