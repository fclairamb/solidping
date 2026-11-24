package incidents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Every generated-title key the server stores must exist in each dash0
// locale, or the dashboard silently falls back to the English literal.
func TestTitleKeysExistInDashboardLocales(t *testing.T) {
	t.Parallel()

	keys := []string{
		TitleKeyDown, TitleKeyDownUnnamed,
		TitleKeyDegradedFailures, TitleKeyDegradedFailuresAvailability, TitleKeyDegradedSlow,
		TitleKeySLOFastBurn, TitleKeySLOSlowBurn,
	}

	for _, lang := range []string{"en", "fr", "de", "es", "zh"} {
		path := filepath.Join("..", "..", "..", "..", "web", "dash0", "src", "locales", lang, "incidents.json")

		content, err := os.ReadFile(path)
		require.NoError(t, err)

		var catalog map[string]any
		require.NoError(t, json.Unmarshal(content, &catalog))

		for _, key := range keys {
			node := catalog["generated"]
			for part := range strings.SplitSeq(key, ".") {
				obj, ok := node.(map[string]any)
				require.True(t, ok, "%s: generated.%s", lang, key)
				node = obj[part]
			}

			value, ok := node.(string)
			require.True(t, ok && value != "", "%s: generated.%s is missing", lang, key)
		}
	}
}

func TestGenerateIncidentTitleKeepsTheRecipe(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc := &Service{}

	check := models.NewCheck("org", "database", "http")
	title, recipe := svc.generateIncidentTitle(check)
	r.Equal("database is down", title)
	r.Equal(TitleKeyDown, recipe.Key)
	r.Equal("database", recipe.Params.Name)

	title, recipe = svc.generateIncidentTitle(&models.Check{})
	r.Equal("Check is down", title)
	r.Equal(TitleKeyDownUnnamed, recipe.Key)
	r.Nil(recipe.Params)
}
