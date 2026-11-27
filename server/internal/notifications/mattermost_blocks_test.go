package notifications

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

func mattermostBlocksOptions() *MattermostBlocksRenderOptions {
	return &MattermostBlocksRenderOptions{
		Channel:       "#alerts",
		Username:      "SolidPing",
		ActionURL:     "https://sp.acme.com/api/v1/integrations/mattermost/actions",
		AckToken:      "ack-token",
		EnableActions: true,
	}
}

// collectMattermostActionIDs walks every nesting level of a block tree. The
// registry is rejected by Mattermost if it names an action no block uses, so
// the test compares this set with the map keys.
func collectMattermostActionIDs(blocks []MattermostBlock) map[string]bool {
	ids := make(map[string]bool)

	var walk func([]MattermostBlock)

	walk = func(items []MattermostBlock) {
		for i := range items {
			if items[i].ActionID != "" {
				ids[items[i].ActionID] = true
			}

			walk(items[i].Content)
			walk(items[i].Header)
			walk(items[i].Items)

			for j := range items[i].Columns {
				walk(items[i].Columns[j].Items)
			}
		}
	}

	walk(blocks)

	return ids
}

func mattermostPostJSON(t *testing.T, post *MattermostBlocksPost) string {
	t.Helper()

	body, err := json.Marshal(post)
	require.NoError(t, err)

	return string(body)
}

func TestMattermostBlocksCreatedCard(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := regionsTestPayload(eventTypeIncidentCreated, true)
	post := BuildMattermostBlocksPost(payload, mattermostBlocksOptions())

	r.Equal("#alerts", post.Channel)
	r.Equal("SolidPing", post.Username)
	body := mattermostPostJSON(t, post)
	r.Contains(body, "[DOWN]")
	r.Contains(body, "API health")
	r.Contains(body, "connection refused")
	r.Equal("urgent", post.Priority.Priority)
	r.Len(post.Props.Blocks, 1)
	r.Equal("container", post.Props.Blocks[0].Type)
	r.Equal("danger", post.Props.Blocks[0].AccentColor)

	ids := collectMattermostActionIDs(post.Props.Blocks)
	r.True(ids[mattermostActionIDAck])
	r.True(ids[mattermostActionIDOpen])
	r.Len(post.Props.Actions, len(ids))
	r.Equal(MattermostActionAcknowledge, post.Props.Actions[mattermostActionIDAck].Context["action"])
	r.Equal("external", post.Props.Actions[mattermostActionIDAck].Type)
	r.Equal("openURL", post.Props.Actions[mattermostActionIDOpen].Type)
}

func TestMattermostBlocksResolvedCardHasOnlyOpen(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := regionsTestPayload(eventTypeIncidentResolved, true)
	payload.Incident.State = models.IncidentStateResolved
	post := BuildMattermostBlocksPost(payload, mattermostBlocksOptions())

	r.Nil(post.Priority, "a recovery must not buzz like a new outage")
	r.Contains(mattermostPostJSON(t, post), "[RECOVERED]")

	ids := collectMattermostActionIDs(post.Props.Blocks)
	r.False(ids[mattermostActionIDAck])
	r.True(ids[mattermostActionIDOpen])
	r.Len(post.Props.Actions, 1)
}

func TestMattermostBlocksDegradedCard(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := degradedTestPayload(eventTypeIncidentCreated, true)
	post := BuildMattermostBlocksPost(payload, mattermostBlocksOptions())

	r.Equal("important", post.Priority.Priority)
	body := mattermostPostJSON(t, post)
	r.Contains(body, "[DEGRADED]")
	r.Contains(body, "Currently up.")
	r.Contains(body, "Window")
	r.NotContains(body, "Failure Count")
}

func TestMattermostBlocksAcknowledgedCard(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := regionsTestPayload(eventTypeIncidentAcknowledged, false)
	now := time.Now()
	payload.Incident.AcknowledgedAt = &now
	payload.Acknowledgment = &AckInfo{ActorName: "alice", Via: "mattermost"}

	post := BuildMattermostBlocksPost(payload, mattermostBlocksOptions())

	r.Contains(mattermostPostJSON(t, post), "Acknowledged by")
	r.Contains(mattermostPostJSON(t, post), "alice")
	ids := collectMattermostActionIDs(post.Props.Blocks)
	r.False(ids[mattermostActionIDAck])
	r.True(ids[mattermostActionIDOpen])
}

func TestMattermostBlocksCommentCardHasOnlyOpen(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := regionsTestPayload(eventTypeIncidentComment, false)
	payload.Comment = &CommentInfo{Text: "checking upstream", AuthorName: "alice"}

	post := BuildMattermostBlocksPost(payload, mattermostBlocksOptions())

	r.Contains(mattermostPostJSON(t, post), "checking upstream")
	ids := collectMattermostActionIDs(post.Props.Blocks)
	r.False(ids[mattermostActionIDAck])
	r.True(ids[mattermostActionIDOpen])
}

func TestMattermostBlocksTestPayloadHasNoActions(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	opts := mattermostBlocksOptions()
	opts.EnableActions = false

	payload := regionsTestPayload(eventTypeIncidentCreated, false)
	post := BuildMattermostBlocksPost(payload, opts)

	r.Empty(post.Props.Actions)
	r.Empty(collectMattermostActionIDs(post.Props.Blocks))
}

func TestMattermostBlocksOutgoingMessageHasNoAttachments(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := regionsTestPayload(eventTypeIncidentCreated, false)
	payload.EventKey = "job-1"
	payload.Integration = &models.Integration{
		UID: "conn-1", OrganizationUID: "org-1", Type: models.ConnectionTypeMattermost,
	}

	jctx := &jobdef.JobContext{
		AppConfig: &config.Config{
			Auth:   config.AuthConfig{JWTSecret: "test-secret"},
			Server: config.ServerConfig{BaseURL: "https://sp.acme.com"},
		},
	}
	settings := &mattermostSettings{MessageFormat: MattermostFormatBlocks}

	msg := (&MattermostSender{}).buildOutgoingMessage(settings, payload, jctx)

	body, err := json.Marshal(msg)
	r.NoError(err)
	r.NotContains(string(body), `"attachments"`)
	r.Contains(string(body), `"mm_blocks"`)
	r.Contains(string(body), `"mm_blocks_actions"`)
	r.Contains(string(body), `"priority":"urgent"`)
}

func TestMattermostBlocksTestPayloadOutgoingMessageHasNoActions(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := regionsTestPayload(eventTypeIncidentCreated, false)
	payload.Integration = &models.Integration{
		UID: "conn-1", OrganizationUID: "org-1", Type: models.ConnectionTypeMattermost,
	}

	jctx := &jobdef.JobContext{
		AppConfig: &config.Config{
			Auth:   config.AuthConfig{JWTSecret: "test-secret"},
			Server: config.ServerConfig{BaseURL: "https://sp.acme.com"},
		},
	}
	settings := &mattermostSettings{MessageFormat: MattermostFormatBlocks}

	msg := (&MattermostSender{}).buildOutgoingMessage(settings, payload, jctx)

	body, err := json.Marshal(msg)
	r.NoError(err)
	r.NotContains(string(body), `"mm_blocks_actions"`)
}

func TestMattermostPriority(t *testing.T) {
	t.Parallel()

	cases := []struct {
		event    string
		degraded bool
		want     string
	}{
		{eventTypeIncidentCreated, false, "urgent"},
		{eventTypeIncidentCreated, true, "important"},
		{eventTypeIncidentEscalated, false, "urgent"},
		{eventTypeIncidentReopened, false, "urgent"},
		{eventTypeIncidentReminder, false, "urgent"},
		{eventTypeIncidentResolved, false, ""},
		{eventTypeIncidentComment, false, ""},
		{eventTypeIncidentAcknowledged, false, ""},
		{eventTypeIncidentUnacknowledged, false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.event+"/"+map[bool]string{true: "degraded", false: "check"}[tc.degraded], func(t *testing.T) {
			t.Parallel()

			priority := mattermostPriorityFor(degradedTestPayload(tc.event, tc.degraded))
			if tc.want == "" {
				require.Nil(t, priority)

				return
			}

			require.NotNil(t, priority)
			require.Equal(t, tc.want, priority.Priority)
		})
	}
}

func TestMattermostActionURL(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		"https://sp.acme.com/api/v1/integrations/mattermost/actions",
		MattermostActionURL("https://sp.acme.com/"))
	require.Empty(t, MattermostActionURL(""))
}
