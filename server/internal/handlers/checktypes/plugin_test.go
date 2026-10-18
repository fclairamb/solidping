package checktypes

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/plugins"
)

// The schema endpoint and the check-types listing serve a plugin type with
// its schema and the plugin's identity (spec 2026-10-17-05 §8).
func TestPluginTypeIsServed(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	set := plugins.Load("../../../../plugins", nil)
	t.Cleanup(set.Close)
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
