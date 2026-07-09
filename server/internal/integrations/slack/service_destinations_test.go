package slack

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestGetDestinationsRejectsTokenlessChannel verifies that a Slack channel with
// no bot token (a manually-created stub) yields ErrSlackNotConnected rather than
// a misleading Slack API / 502 error.
func TestGetDestinationsRejectsTokenlessChannel(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	org := models.NewOrganization("slack-dest-test", "Slack Dest Test Org")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	conn := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "Slack")
	// Tokenless stub: empty settings, no access_token.
	conn.Settings = models.JSONMap{}
	r.NoError(svc.db.CreateChannel(ctx, conn))

	_, err := svc.GetDestinations(ctx, org.Slug, conn.UID)
	r.ErrorIs(err, ErrSlackNotConnected)
}

// TestGetDestinationsAggregatesAndSorts verifies that GetDestinations
// aggregates channels across Slack pages and returns channels sorted by name
// and users sorted by display name (real name falling back to username),
// case-insensitively — Slack's own order is arbitrary.
func TestGetDestinationsAggregatesAndSorts(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/conversations.list", func(w http.ResponseWriter, req *http.Request) {
		// Two pages, both unsorted: sorting must happen over the aggregate.
		var payload map[string]any
		_ = json.NewDecoder(req.Body).Decode(&payload)

		if cursor, _ := payload["cursor"].(string); cursor == "" {
			writeSlackJSON(t, w, map[string]any{
				"channels": []map[string]any{
					{"id": "C1", "name": "zulu"},
					{"id": "C2", "name": "Mike"},
				},
				"response_metadata": map[string]any{"next_cursor": "page-2"},
			})

			return
		}

		writeSlackJSON(t, w, map[string]any{
			"channels": []map[string]any{{"id": "C3", "name": "alpha"}},
		})
	})
	mux.HandleFunc("/users.list", func(w http.ResponseWriter, _ *http.Request) {
		writeSlackJSON(t, w, map[string]any{
			"members": []map[string]any{
				{"id": "U1", "name": "zeta", "real_name": "Walter White"},
				// No real name: the username is the display name.
				{"id": "U2", "name": "adam"},
				{"id": "U3", "name": "nn", "real_name": "bob Ross"},
			},
		})
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	svc.newAPIClient = func(token string) *Client {
		return NewClientWithBaseURL(token, server.URL)
	}

	org := models.NewOrganization("slack-dest-sort", "Slack Dest Sort Org")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	conn := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "Slack")
	conn.Settings = models.JSONMap{"access_token": "xoxb-test-token"}
	r.NoError(svc.db.CreateChannel(ctx, conn))

	resp, err := svc.GetDestinations(ctx, org.Slug, conn.UID)
	r.NoError(err)

	channelNames := make([]string, 0, len(resp.Channels))
	for _, ch := range resp.Channels {
		channelNames = append(channelNames, ch.Name)
	}

	r.Equal([]string{"alpha", "Mike", "zulu"}, channelNames)

	userIDs := make([]string, 0, len(resp.Users))
	for _, u := range resp.Users {
		userIDs = append(userIDs, u.ID)
	}

	// adam < bob Ross < Walter White on display name, case-insensitively.
	r.Equal([]string{"U2", "U3", "U1"}, userIDs)
}
