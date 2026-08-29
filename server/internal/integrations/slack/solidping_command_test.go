package slack

import (
	"context"
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

	for _, want := range []string{"checks add", "checks list", "checks rm", "results", "incidents list", "config default-channel"} {
		r.Contains(body, want)
	}

	for _, mustNot := range []string{"setup", "`ack`"} {
		r.NotContains(body, mustNot)
	}
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

// TestSolidpingCommand_CheckCreatesCheck covers both URL shapes the spec
// calls out: an explicit scheme, and a bare host that handleCheckCommand
// normalizes by prefixing https://. handleCheckCommand posts its
// confirmation via a real Slack API call (unchanged production behavior —
// this spec only moves /check's entry point, not its internals), which has
// no reachable target in this test environment; a bounded context keeps that
// attempt from stalling the test, and the assertion is the side effect that
// matters here — the check actually landing in the database — not the exact
// shape of the (network-dependent) reply.
func TestSolidpingCommand_CheckCreatesCheck(t *testing.T) {
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

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			_, err := h.handleSolidpingCommand(ctx, solidpingCmd(tc.text))
			r.NoError(err)

			listed, err := svc.checksService.ListChecks(t.Context(), org.Slug, checks.ListChecksOptions{})
			r.NoError(err)
			r.Len(listed.Data, 1, "check must be created regardless of whether the confirmation post reaches Slack")
		})
	}
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
// section: "the ambiguous-incident path -> unchanged behaviour").
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
