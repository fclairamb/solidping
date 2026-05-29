package slack

import (
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
