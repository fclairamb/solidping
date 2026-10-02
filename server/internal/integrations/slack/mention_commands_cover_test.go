package slack

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// The subtests share the handler's respond hook (and its captured output), so they must run sequentially.
//
//nolint:paralleltest,tparallel // shared h.respond capture
func TestMentionCommandsCover(t *testing.T) {
	t.Parallel()

	h, svc, org := solidpingSetup(t)
	coverFakeSlack(t, svc)

	var got []string
	h.respond = func(msg *MessageResponse) error {
		var text strings.Builder

		text.WriteString(msg.Text)

		for i := range msg.Blocks {
			if msg.Blocks[i].Text != nil {
				text.WriteString("\n" + msg.Blocks[i].Text.Text)
			}
		}

		got = append(got, text.String())

		return nil
	}

	ctx := t.Context()
	chk := models.NewCheck(org.UID, "api", "http")
	require.NoError(t, svc.db.CreateCheck(ctx, chk))

	quiet := models.NewCheck(org.UID, "quiet", "http")
	require.NoError(t, svc.db.CreateCheck(ctx, quiet))

	gone := models.NewCheck(org.UID, "gone", "http")
	require.NoError(t, svc.db.CreateCheck(ctx, gone))

	for _, st := range []models.ResultStatus{models.ResultStatusUp, models.ResultStatusDown} {
		require.NoError(t, svc.db.CreateResult(ctx, models.NewResult(org.UID, chk.UID, st, 12)))
	}

	active := models.NewIncident(org.UID, chk.UID, time.Now().Add(-time.Hour), "down")
	require.NoError(t, svc.db.CreateIncident(ctx, active))

	resolvedAt := time.Now().Add(-time.Minute)
	resolved := models.NewIncident(org.UID, chk.UID, time.Now().Add(-2*time.Hour), "old")
	resolved.State = models.IncidentStateResolved
	resolved.ResolvedAt = &resolvedAt
	require.NoError(t, svc.db.CreateIncident(ctx, resolved))

	ev := &Event{TeamID: msgTeamID, Event: EventPayload{Channel: msgChannel, Ts: "1.1"}}
	badTeam := &Event{TeamID: "nope", Event: EventPayload{Channel: msgChannel, Ts: "1.1"}}

	tests := []struct {
		name string
		ev   *Event
		cmd  ParsedCommand
		want string
	}{
		{"unknown", ev, ParsedCommand{Command: "zzz"}, "Unknown command"},
		{"help", ev, ParsedCommand{Command: "help"}, "Available Commands"},
		{"checks unknown sub", ev, ParsedCommand{Command: "checks", Subcommand: "zzz"}, "Unknown checks subcommand"},
		{"checks list", ev, ParsedCommand{Command: "checks", Subcommand: "list"}, "checks"},
		{"checks list no team", badTeam, ParsedCommand{Command: "checks"}, "not connected"},
		{"checks add missing", ev, ParsedCommand{Command: "checks", Subcommand: "add"}, "Missing URL"},
		{"checks add fake", ev, ParsedCommand{Command: "checks", Subcommand: "add", Args: []string{"fake"}}, "added"},
		{"checks add host", ev, ParsedCommand{Command: "checks", Subcommand: "add", Args: []string{"acme.com"}}, "added"},
		{
			"checks add no team", badTeam,
			ParsedCommand{Command: "checks", Subcommand: "add", Args: []string{"acme.com"}},
			"Failed to create",
		},
		{"checks rm missing", ev, ParsedCommand{Command: "checks", Subcommand: "rm"}, "Missing check slug"},
		{
			"checks rm no team", badTeam,
			ParsedCommand{Command: "checks", Subcommand: "rm", Args: []string{"x"}},
			"not connected",
		},
		{
			"checks rm unknown", ev,
			ParsedCommand{Command: "checks", Subcommand: "rm", Args: []string{"nonexistent"}},
			"Failed to remove",
		},
		{"checks rm ok", ev, ParsedCommand{Command: "checks", Subcommand: "rm", Args: []string{"gone"}}, "removed"},
		{"results missing", ev, ParsedCommand{Command: "results"}, "Missing check"},
		{"results no team", badTeam, ParsedCommand{Command: "results", Args: []string{"api"}}, "not connected"},
		{"results unknown", ev, ParsedCommand{Command: "results", Args: []string{"zzz"}}, "not found"},
		{"results ok", ev, ParsedCommand{Command: "results", Flags: map[string]string{"check": "api"}}, "Results for api"},
		{"incidents bad sub", ev, ParsedCommand{Command: "incidents", Subcommand: "zzz"}, "Unknown incidents subcommand"},
		{"incidents no team", badTeam, ParsedCommand{Command: "incidents"}, "not connected"},
		{"incidents unknown check", ev, ParsedCommand{Command: "incidents", Args: []string{"zzz"}}, "not found"},
		{"incidents list", ev, ParsedCommand{Command: "incidents", Subcommand: "list"}, "2 incidents"},
		{
			"incidents for check", ev,
			ParsedCommand{Command: "incidents", Flags: map[string]string{"check": "api"}},
			"2 incidents",
		},
		{"incidents none", ev, ParsedCommand{Command: "incidents", Args: []string{"quiet"}}, "No incidents found for check"},
		{"config missing", ev, ParsedCommand{Command: "config"}, "Missing config option"},
		{"config unknown", ev, ParsedCommand{Command: "config", Subcommand: "zzz"}, "Unknown config option"},
		{
			"config default current", ev,
			ParsedCommand{Command: "config", Subcommand: "default-channel"},
			"Default channel updated",
		},
		{
			"config default ref", ev,
			ParsedCommand{Command: "config", Subcommand: "default-channel", Args: []string{"<#C-NEW|alerts>"}},
			"Default channel updated",
		},
		{
			"config default invalid", ev,
			ParsedCommand{Command: "config", Subcommand: "default-channel", Args: []string{"alerts"}},
			"Invalid channel",
		},
		{"config default no team", badTeam, ParsedCommand{Command: "config", Subcommand: "default-channel"}, "Failed to set"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got = nil
			cmd := tt.cmd
			require.NoError(t, h.handleMentionCommand(t.Context(), tt.ev, &cmd))
			require.NotEmpty(t, got)
			require.Contains(t, strings.Join(got, "\n"), tt.want)
		})
	}
}

func TestMentionHelpersCover(t *testing.T) {
	t.Parallel()

	for ref, want := range map[string]string{
		"<#C123|general>": "C123", "<#C123>": "C123", "C123": "C123", "C": "", "general": "", "": "",
	} {
		require.Equal(t, want, parseChannelReference(ref), ref)
	}

	i := func(v models.ResultStatus) *int { n := int(v); return &n }
	unknown := 999
	require.Equal(t, "unknown", statusIntToString(nil))
	require.Equal(t, statusUp, statusIntToString(i(models.ResultStatusUp)))
	require.Equal(t, statusWarning, statusIntToString(i(models.ResultStatusWarning)))
	require.Equal(t, statusDown, statusIntToString(i(models.ResultStatusDown)))
	require.Equal(t, statusDown, statusIntToString(i(models.ResultStatusTimeout)))
	require.Equal(t, statusDown, statusIntToString(i(models.ResultStatusError)))
	require.Equal(t, "unknown", statusIntToString(&unknown))
}
