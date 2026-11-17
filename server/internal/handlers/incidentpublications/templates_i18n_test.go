package incidentpublications

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The public narrative templates live here (mails, feeds, webhooks and the
// literal columns) AND in the two frontends, which render the stored recipes
// in the reader's language. The copies must say the same thing, or a German
// visitor of an English page would read a different sentence than the German
// subscriber mail of a German page.
func TestPublicTemplatesMatchFrontendLocales(t *testing.T) {
	t.Parallel()

	webDir := filepath.Join("..", "..", "..", "..", "web")

	catalogs := map[string]func(lang string) map[string]any{
		"status0": func(lang string) map[string]any {
			return readLocale(t, filepath.Join(webDir, "status0", "src", "locales", lang, "status.json"), "publication")
		},
		"dash0": func(lang string) map[string]any {
			return readLocale(t, filepath.Join(webDir, "dash0", "src", "locales", lang, "incidents.json"),
				"generated", "publication")
		},
	}

	for lang, set := range publicTemplates {
		for app, load := range catalogs {
			entries := load(lang)
			require.Len(t, entries, len(set), "%s/%s: publication template count", app, lang)

			for key, format := range set {
				want := strings.ReplaceAll(format, "%s", "{{name}}")
				require.Equal(t, want, entries[string(key)], "%s/%s: publication.%s", app, lang, key)
			}
		}
	}
}

func TestRenderKeepsTheRecipe(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	de := templatesFor(new("de-DE"))

	named := de.render(keyOpenedTitle, "Datenbank")
	r.Equal("Datenbank hat Probleme", named.Value)
	r.Equal(I18nKeyPrefix+"openedTitle", named.I18n.Key)
	r.Equal("Datenbank", named.I18n.Params.Name)

	unnamed := de.render(keyResolvedTitle, "")
	r.Equal("Einige Dienste haben Probleme ist wiederhergestellt", unnamed.Value,
		"an unnamed component reads as the group title, as before")
	r.Nil(unnamed.I18n.Params, "the client substitutes its own group title")

	plain := de.render(keyMonitoringBody, "Datenbank")
	r.NotContains(plain.Value, "%")
	r.Nil(plain.I18n.Params, "a template without a slot carries no name")
}

func readLocale(t *testing.T, path string, keys ...string) map[string]any {
	t.Helper()

	content, err := os.ReadFile(path) //nolint:gosec // fixed test path.
	require.NoError(t, err)

	var node map[string]any
	require.NoError(t, json.Unmarshal(content, &node))

	for _, key := range keys {
		next, ok := node[key].(map[string]any)
		require.True(t, ok, "%s: missing %q", path, key)

		node = next
	}

	return node
}
