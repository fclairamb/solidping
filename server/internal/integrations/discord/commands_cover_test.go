package discord

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestDispatchCommandCover(t *testing.T) {
	t.Parallel()

	ctx, svc, _, orgUID, fake := installedServiceWithDiscord(t)
	guild := fake.guild.ID

	checkUID := seedCheck(ctx, t, svc, orgUID, "api")
	seedCheck(ctx, t, svc, orgUID, "quiet")
	seedCheck(ctx, t, svc, orgUID, "gone")

	active := models.NewIncident(orgUID, checkUID, time.Now().Add(-time.Hour), "down")
	require.NoError(t, svc.db.CreateIncident(ctx, active))

	resolvedAt := time.Now().Add(-time.Minute)
	resolved := models.NewIncident(orgUID, checkUID, time.Now().Add(-2*time.Hour), "old")
	resolved.State = models.IncidentStateResolved
	resolved.ResolvedAt = &resolvedAt
	require.NoError(t, svc.db.CreateIncident(ctx, resolved))

	noEnd := models.NewIncident(orgUID, checkUID, time.Now().Add(-3*time.Hour), "old2")
	noEnd.State = models.IncidentStateResolved
	require.NoError(t, svc.db.CreateIncident(ctx, noEnd))

	tests := []struct {
		name string
		cmd  Command
		want string
	}{
		{"checks add missing", Command{Command: "checks", Subcommand: "add"}, "Missing URL"},
		{"checks add invalid", Command{Command: "checks", Subcommand: "add", Args: []string{"http://"}}, "Invalid URL"},
		{"checks add blank", Command{Command: "check", Args: []string{"  "}}, "Invalid URL"},
		{"checks add ok", Command{Command: "check", Args: []string{"acme.com"}}, "created"},
		{
			"checks add unconnected",
			Command{Command: "check", GuildID: "G-NOPE", Args: []string{"acme.com"}},
			"Failed to create",
		},
		{"checks list", Command{Command: "checks", Subcommand: "ls"}, "checks"},
		{"checks list unconnected", Command{Command: "checks", GuildID: "G-NOPE"}, "not connected"},
		{"checks rm missing", Command{Command: "checks", Subcommand: "rm"}, "Missing check slug"},
		{
			"checks rm unconnected",
			Command{Command: "checks", Subcommand: "rm", GuildID: "G-NOPE", Args: []string{"x"}},
			"not connected",
		},
		{"checks rm unknown", Command{Command: "checks", Subcommand: "delete", Args: []string{"zzz"}}, "Failed to remove"},
		{"checks rm ok", Command{Command: "checks", Subcommand: "remove", Args: []string{"gone"}}, "removed"},
		{"checks bad sub", Command{Command: "checks", Subcommand: "zzz"}, "Unknown checks subcommand"},
		{"incidents bad sub", Command{Command: "incidents", Subcommand: "zzz"}, "Unknown incidents subcommand"},
		{"incidents unconnected", Command{Command: "incidents", GuildID: "G-NOPE"}, "not connected"},
		{"incidents unknown check", Command{Command: "incidents", Args: []string{"zzz"}}, "not found"},
		{"incidents list", Command{Command: "incidents", Subcommand: "list"}, "3 incidents"},
		{"incidents by flag", Command{Command: "incidents", Flags: map[string]string{"check": "api"}}, "3 incidents"},
		{"incidents none", Command{Command: "incidents", Args: []string{"quiet"}}, "No incidents"},
		{"config usage", Command{Command: "config"}, "Usage"},
		{"config no args", Command{Command: "config", Subcommand: "default-channel"}, "Usage"},
		{
			"config bad channel",
			Command{Command: "config", Subcommand: "default-channel", Args: []string{"nope"}},
			"Could not set that channel",
		},
		{"help", Command{Command: "help"}, "SolidPing"},
		{"unknown", Command{Command: "zzz"}, "Unknown command"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cmd := tt.cmd
			if cmd.GuildID == "" {
				cmd.GuildID = guild
			}

			resp, err := DispatchCommand(t.Context(), svc, &cmd)
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.Contains(t, resp.Text, tt.want)
		})
	}
}

func TestBotClientCover(t *testing.T) {
	t.Parallel()

	var limited atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/rate":
			if limited.Add(1) == 1 {
				w.Header().Set("Retry-After", "0.01")
				w.WriteHeader(http.StatusTooManyRequests)

				return
			}
			_, _ = w.Write([]byte(`{"id":"ok"}`))
		case "/channels/missing":
			w.WriteHeader(http.StatusNotFound)
		case "/channels/forbidden":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":50007,"message":"cannot dm"}`))
		case "/channels/garbage":
			_, _ = w.Write([]byte(`not json`))
		default:
			_, _ = w.Write([]byte(`{"id":"X","name":"n","type":0}`))
		}
	}))
	t.Cleanup(srv.Close)

	ctx := t.Context()
	c := NewBotClient("tok").WithBaseURL(srv.URL)

	require.NoError(t, c.EditMessage(ctx, "C1", "M1", &Message{Content: "x"}))

	thread, err := c.StartThreadFromMessage(ctx, "C1", "M1", string(make([]rune, 150)))
	require.NoError(t, err)
	require.NotNil(t, thread)

	guild, err := c.GetGuild(ctx, "G1")
	require.NoError(t, err)
	require.NotNil(t, guild)

	channel, err := c.GetChannel(ctx, "C1")
	require.NoError(t, err)
	require.NotNil(t, channel)

	require.NoError(t, c.do(ctx, http.MethodGet, "/rate", nil, &struct{}{}))
	require.Equal(t, int32(2), limited.Load())

	_, err = c.GetChannel(ctx, "missing")
	require.ErrorIs(t, err, ErrNotFound)

	_, err = c.GetChannel(ctx, "forbidden")
	require.Error(t, err)
	require.True(t, IsCannotDMUser(err))
	require.Contains(t, err.Error(), "50007")
	require.False(t, IsCannotDMUser(ErrNotFound))

	_, err = c.GetChannel(ctx, "garbage")
	require.Error(t, err)

	require.ErrorIs(t, NewBotClient("").EditMessage(ctx, "C", "M", &Message{}), ErrBotTokenMissing)

	resp := &http.Response{Header: http.Header{}}
	require.Equal(t, time.Second, retryAfter(resp))
	resp.Header.Set("Retry-After", "abc")
	require.Equal(t, time.Second, retryAfter(resp))
	resp.Header.Set("Retry-After", "100")
	require.Equal(t, 5*time.Second, retryAfter(resp))
	resp.Header.Set("Retry-After", "2")
	require.Equal(t, 2*time.Second, retryAfter(resp))

	require.Len(t, truncateThreadName(string(make([]rune, 150))), 100)
	require.Equal(t, "short", truncateThreadName("short"))

	row := IncidentActionRow("uid-1")
	require.Len(t, row.Components, 3)
}
