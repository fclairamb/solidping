package slack

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// solidpingSetup gives a Handler backed by a real in-memory DB, a real
// checks.Service (so `check`/`list`/`create` exercise the production code
// path, not a fake), and a Slack connection for msgTeamID/msgChannel.
func solidpingSetup(t *testing.T) (*Handler, *Service, *models.Organization) {
	t.Helper()
	r := require.New(t)
	ctx, svc := setupSlackService(t)

	svc.checksService = checks.NewService(svc.db, nil, nil, nil)

	org := models.NewOrganization("slack-solidping-cmd", "")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	seedSlackConnection(t, svc, org.UID, models.SlackCommentIngestionExplicit)

	return &Handler{svc: svc}, svc, org
}

// solidpingCmd builds a `/solidping <text>` slash Command against the seeded
// workspace/channel.
func solidpingCmd(text string) *Command {
	return &Command{
		Command:   "/solidping",
		Text:      text,
		TeamID:    msgTeamID,
		ChannelID: msgChannel,
		UserID:    "U-ALICE",
		UserName:  "alice",
	}
}

func TestSolidpingCommand_EmptyTextReturnsEphemeralHelp(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h, _, _ := solidpingSetup(t)

	resp, err := h.handleSolidpingCommand(t.Context(), solidpingCmd(""))
	r.NoError(err)
	r.NotNil(resp)
	r.Equal(ResponseTypeEphemeral, resp.ResponseType)
	r.Contains(resp.Text, "SolidPing Help")
}

// TestSolidpingCommand_HelpListsExistingSubcommands pins that `/solidping
// help` lists exactly the subcommands the router actually implements, and
// never the two dropped from the manifest hint (setup, ack) — the mismatch
// between advertised and implemented subcommands is the root cause of this
// spec.
func TestSolidpingCommand_HelpListsExistingSubcommands(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h, _, _ := solidpingSetup(t)

	resp, err := h.handleSolidpingCommand(t.Context(), solidpingCmd("help"))
	r.NoError(err)
	r.NotNil(resp)
	r.Equal(ResponseTypeEphemeral, resp.ResponseType)
	r.NotEmpty(resp.Blocks)
	r.NotNil(resp.Blocks[0].Text)
	body := resp.Blocks[0].Text.Text

	want := []string{
		"checks add", "checks list", "checks rm", "results", "incidents list", "config default-channel",
	}
	for _, w := range want {
		r.Contains(body, w)
	}

	for _, mustNot := range []string{"setup", "`ack`"} {
		r.NotContains(body, mustNot)
	}

	// /solidping help must show /solidping-syntax examples, not the
	// app_mention transport's @solidping mention syntax — a Marketplace
	// reviewer (and any real user) types this in the first five seconds.
	r.Contains(body, "`/solidping checks add")
	r.NotContains(body, "@solidping")
}

func TestSolidpingCommand_UnknownSubcommandNamesHelp(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h, _, _ := solidpingSetup(t)

	resp, err := h.handleSolidpingCommand(t.Context(), solidpingCmd("frobnicate"))
	r.NoError(err)
	r.NotNil(resp)
	r.Equal(ResponseTypeEphemeral, resp.ResponseType)
	r.Contains(resp.Text, "Unknown")
	r.Contains(resp.Text, "help")
}

func TestSolidpingCommand_CheckWithNoArgumentReturnsUsageNotError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h, _, _ := solidpingSetup(t)

	resp, err := h.handleSolidpingCommand(t.Context(), solidpingCmd("check"))
	r.NoError(err)
	r.NotNil(resp)
	r.Equal(ResponseTypeEphemeral, resp.ResponseType)
	r.Contains(resp.Text, "Usage")
	r.Contains(resp.Text, "/solidping check")
}

// TestSolidpingCommand_CheckWithNoResponseURLFallsBackSynchronously covers
// both URL shapes the spec calls out (an explicit scheme, and a bare host
// that normalizeCheckURL prefixes with https://) through the defensive
// synchronous fallback: solidpingCmd sets no ResponseURL, so
// handleCheckCommand cannot ACK-then-follow-up and does the work inline
// instead. This path makes no outbound network call (the old bot-token
// chat.postMessage confirmation was replaced by the response_url follow-up —
// see TestSolidpingCommand_CheckAsyncFollowUp — and the fallback used here
// skips that mechanism entirely), so it is fully deterministic.
func TestSolidpingCommand_CheckWithNoResponseURLFallsBackSynchronously(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		text string
	}{
		{name: "explicit scheme", text: "check https://example.com"},
		{name: "bare host", text: "check example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			h, svc, org := solidpingSetup(t)

			resp, err := h.handleSolidpingCommand(t.Context(), solidpingCmd(tc.text))
			r.NoError(err)
			r.NotNil(resp)
			r.Equal(ResponseTypeInChannel, resp.ResponseType)
			r.Contains(resp.Text, "Check created")

			listed, err := svc.checksService.ListChecks(t.Context(), org.Slug, checks.ListChecksOptions{})
			r.NoError(err)
			r.Len(listed.Data, 1)
		})
	}
}

// responseURLRecorder starts an httptest server standing in for Slack's
// response_url and hands back every posted MessageResponse over a channel,
// so a test can block on the async follow-up without polling or a fixed
// sleep. Slack's real response_url needs no auth — this fake doesn't check
// for any, matching production.
func responseURLRecorder(t *testing.T) (string, chan *MessageResponse) {
	t.Helper()

	received := make(chan *MessageResponse, 4)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var msg MessageResponse
		if err := json.NewDecoder(req.Body).Decode(&msg); err != nil {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		received <- &msg
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	return server.URL, received
}

// TestSolidpingCommand_CheckAsyncFollowUp is the coordinator-flagged
// regression test: Slack's 3-second ACK budget requires `/solidping check`
// (a DB write, formerly followed by a synchronous outbound Slack API call)
// to ACK immediately and report the real outcome — success or failure —
// via response_url afterward, never a fake "it worked" swallowing a later
// failure.
func TestSolidpingCommand_CheckAsyncFollowUp(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		h, svc, org := solidpingSetup(t)
		responseURL, received := responseURLRecorder(t)

		cmd := solidpingCmd("check https://example.com")
		cmd.ResponseURL = responseURL

		start := time.Now()
		ack, err := h.handleSolidpingCommand(t.Context(), cmd)
		elapsed := time.Since(start)

		r.NoError(err)
		r.NotNil(ack)
		r.Equal(ResponseTypeEphemeral, ack.ResponseType)
		r.Less(elapsed, time.Second, "the ACK must return immediately, not wait on the DB write")

		var followUp *MessageResponse
		select {
		case followUp = <-received:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the response_url follow-up")
		}

		r.Equal(ResponseTypeInChannel, followUp.ResponseType)
		r.Contains(followUp.Text, "Check created")

		listed, err := svc.checksService.ListChecks(t.Context(), org.Slug, checks.ListChecksOptions{})
		r.NoError(err)
		r.Len(listed.Data, 1)
	})

	t.Run("failure is reported, not swallowed", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		// setupSlackService without solidpingSetup's org/connection: the
		// workspace is unrecognized, so CreateCheck fails at
		// GetConnectionByTeamID.
		_, svc := setupSlackService(t)
		svc.checksService = checks.NewService(svc.db, nil, nil, nil)
		h := &Handler{svc: svc}

		responseURL, received := responseURLRecorder(t)

		cmd := solidpingCmd("check https://example.com")
		cmd.ResponseURL = responseURL

		ack, err := h.handleSolidpingCommand(t.Context(), cmd)
		r.NoError(err)
		r.NotNil(ack)
		r.Equal(ResponseTypeEphemeral, ack.ResponseType)

		var followUp *MessageResponse
		select {
		case followUp = <-received:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the response_url follow-up")
		}

		r.Equal(ResponseTypeEphemeral, followUp.ResponseType,
			"a failure must be reported ephemerally, never as a cheerful in-channel success")
		r.Contains(followUp.Text, "not connected")
	})
}

func TestSolidpingCommand_CommentSingleTrackedIncident(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h, svc, org := solidpingSetup(t)
	incident := openIncident(t, svc, org.UID, "api is down", "1700000010.000001")

	fake := &fakeIncidentService{}
	svc.incidentsService = fake

	resp, err := h.handleSolidpingCommand(t.Context(), solidpingCmd("comment central DNS is down"))
	r.NoError(err)
	r.NotNil(resp)
	r.Equal(ResponseTypeEphemeral, resp.ResponseType)
	r.Len(fake.commandComments, 1)
	r.Equal(incident.UID, fake.commandComments[0].incidentUID)
}

// TestSolidpingCommand_CommentAmbiguousPath pins that the disambiguation
// behavior is unchanged under the new /solidping prefix (spec Testing
// section: "the ambiguous-incident path -> unchanged behavior").
func TestSolidpingCommand_CommentAmbiguousPath(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h, svc, org := solidpingSetup(t)
	openIncident(t, svc, org.UID, "api is down", "1700000011.000001")
	openIncident(t, svc, org.UID, "db is down", "1700000011.000002")

	fake := &fakeIncidentService{}
	svc.incidentsService = fake

	resp, err := h.handleSolidpingCommand(t.Context(), solidpingCmd("comment both of these look bad"))
	r.NoError(err)
	r.NotNil(resp)
	r.Equal(ResponseTypeEphemeral, resp.ResponseType)
	r.Contains(resp.Text, "Several active incidents")
	r.Empty(fake.commandComments, "an ambiguous reference must never write a comment")
}

func TestSolidpingCommand_ListAliasesToChecksList(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h, _, _ := solidpingSetup(t)

	resp, err := h.handleSolidpingCommand(t.Context(), solidpingCmd("list"))
	r.NoError(err)
	r.NotNil(resp)
	r.Equal(ResponseTypeEphemeral, resp.ResponseType)
	r.Contains(resp.Text, "No checks found")
}

func TestSolidpingCommand_CreateAliasesToChecksAdd(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h, svc, org := solidpingSetup(t)

	resp, err := h.handleSolidpingCommand(t.Context(), solidpingCmd("create https://example.com"))
	r.NoError(err)
	r.NotNil(resp)
	r.Equal(ResponseTypeEphemeral, resp.ResponseType)
	r.Contains(resp.Text, "added")

	listed, err := svc.checksService.ListChecks(t.Context(), org.Slug, checks.ListChecksOptions{})
	r.NoError(err)
	r.Len(listed.Data, 1)
}

func TestSolidpingCommand_ConfigAndIncidentsReachTheRouter(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h, _, _ := solidpingSetup(t)

	configResp, err := h.handleSolidpingCommand(t.Context(), solidpingCmd("config"))
	r.NoError(err)
	r.NotNil(configResp)
	r.Equal(ResponseTypeEphemeral, configResp.ResponseType)
	r.NotContains(configResp.Text, "Unknown command")

	incidentsResp, err := h.handleSolidpingCommand(t.Context(), solidpingCmd("incidents"))
	r.NoError(err)
	r.NotNil(incidentsResp)
	r.Equal(ResponseTypeEphemeral, incidentsResp.ResponseType)
	r.NotContains(incidentsResp.Text, "Unknown command")
}

// TestDispatchCommand_LegacyCheckReturnsMovedNotice mirrors
// TestDispatchCommand_LegacyCommentReturnsMovedNotice (comment_command_test.go):
// the retired standalone /check must answer with a moved notice and take no
// action, not silently keep creating checks.
func TestDispatchCommand_LegacyCheckReturnsMovedNotice(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, svc, org := solidpingSetup(t)

	resp, err := DispatchCommand(t.Context(), svc, &Command{
		Command: "/check", Text: "https://example.com",
		TeamID: msgTeamID, ChannelID: msgChannel, UserID: "U-ALICE",
	})
	r.NoError(err)
	r.NotNil(resp)
	r.Equal(ResponseTypeEphemeral, resp.ResponseType)
	r.Contains(resp.Text, "/solidping check")

	listed, err := svc.checksService.ListChecks(t.Context(), org.Slug, checks.ListChecksOptions{})
	r.NoError(err)
	r.Empty(listed.Data, "the retired /check must not create a check anymore")
}

func TestDispatchCommand_RoutesSolidpingToHandler(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, svc, _ := solidpingSetup(t)

	resp, err := DispatchCommand(t.Context(), svc, &Command{
		Command: "/solidping", Text: "help",
		TeamID: msgTeamID, ChannelID: msgChannel, UserID: "U-ALICE",
	})
	r.NoError(err)
	r.NotNil(resp)
	r.Equal(ResponseTypeEphemeral, resp.ResponseType)
	r.Contains(resp.Text, "SolidPing Help")
}
