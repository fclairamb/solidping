package notifications

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/slack"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// TestStoreThreadInfo_WritesReverseMapping verifies that recording an incident's
// Slack thread writes both the forward incident→thread entry and the reverse
// thread→incident entry that inbound replies resolve through.
func TestStoreThreadInfo_WritesReverseMapping(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	settings := models.SlackSettings{AccessToken: "xoxb-test", TeamID: "T-REV"}
	settingsMap, err := settings.ToJSONMap()
	r.NoError(err)

	payload := &Payload{
		Integration: &models.Integration{Settings: settingsMap},
		Incident:    &models.Incident{UID: "inc-rev-1", OrganizationUID: "org-rev-1"},
	}

	db := &mockDBService{}
	jctx := &jobdef.JobContext{DBService: db, Logger: slog.Default()}

	const forwardKey = "incidents/inc-rev-1/slack/thread"
	result := &slack.PostMessageResult{Channel: "C-REV", TS: "1700000000.000123"}

	err = (&SlackSender{}).storeThreadInfo(context.Background(), jctx, payload, forwardKey, result)
	r.NoError(err)

	// Both the forward and reverse entries must have been written.
	r.Len(db.setStateCalls, 2)

	reverseKey := slack.ReverseThreadStateKey("T-REV", "C-REV", "1700000000.000123")

	var forward, reverse *setStateCall
	for i := range db.setStateCalls {
		switch db.setStateCalls[i].key {
		case forwardKey:
			forward = &db.setStateCalls[i]
		case reverseKey:
			reverse = &db.setStateCalls[i]
		}
	}

	r.NotNil(forward, "forward incident→thread entry must be written")
	r.NotNil(reverse, "reverse thread→incident entry must be written")

	r.Equal("inc-rev-1", (*reverse.value)[slack.ThreadIncidentUIDKey])
	r.Equal("org-rev-1", (*reverse.value)[slack.ThreadOrgUIDKey])
}
