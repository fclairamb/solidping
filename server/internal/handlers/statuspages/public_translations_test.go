package statuspages

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

type fakeIncidentProvider struct{ incidents []PublicIncident }

func (f fakeIncidentProvider) ListPublicIncidents(
	context.Context, *models.StatusPage, bool,
) ([]PublicIncident, error) {
	return f.incidents, nil
}

// TestPublicViewCarriesTranslations pins that the hand-written translations
// reach the public response, on both the recentUpdates and incidents projections.
func TestPublicViewCarriesTranslations(t *testing.T) {
	t.Parallel()

	ctx, svc, _, org := memoTestSetup(t)
	page := seedMemoPage(ctx, t, svc, org)

	upd := models.NewStatusUpdate(org.UID, page.UID, "")
	upd.Title = "Investigating"
	upd.BodyMarkdown = "body"
	upd.Kind = models.StatusUpdateKindInvestigating
	upd.Translations = models.Translations{"fr": {Title: "Enquete", Body: "corps"}}
	require.NoError(t, svc.db.CreateStatusUpdate(ctx, upd))

	svc.SetPublicIncidentProvider(fakeIncidentProvider{incidents: []PublicIncident{{
		UID:          "inc-1",
		Title:        "Outage",
		State:        "open",
		Translations: models.Translations{"fr": {Title: "Panne"}},
		Updates: []PublicIncidentUpdate{{
			UID: "upd-1", Kind: "update", Title: "Update",
			Translations: models.Translations{"fr": {Title: "Mise a jour", Body: "texte"}},
		}},
	}}})

	view, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
	require.NoError(t, err)

	raw, err := json.Marshal(view)
	require.NoError(t, err)

	var wire struct {
		RecentUpdates []struct {
			Translations map[string]map[string]string `json:"translations"`
		} `json:"recentUpdates"`
		ActiveIncidents []struct {
			Translations map[string]map[string]string `json:"translations"`
			Updates      []struct {
				Translations map[string]map[string]string `json:"translations"`
			} `json:"updates"`
		} `json:"activeIncidents"`
	}
	require.NoError(t, json.Unmarshal(raw, &wire))

	require.Len(t, wire.RecentUpdates, 1)
	require.Equal(t, "Enquete", wire.RecentUpdates[0].Translations["fr"]["title"])
	require.Equal(t, "corps", wire.RecentUpdates[0].Translations["fr"]["body"])

	require.Len(t, wire.ActiveIncidents, 1)
	require.Equal(t, "Panne", wire.ActiveIncidents[0].Translations["fr"]["title"])
	require.Equal(t, "texte", wire.ActiveIncidents[0].Updates[0].Translations["fr"]["body"])
}
