package telegram_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
)

// TestParseIncidentRef covers the reference forms a human actually types. The
// '#' is optional because phone keyboards bury it, and because copy-pasting
// "#42" out of a Slack alert is the other half of the same workflow.
func TestParseIncidentRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  int64
		ok    bool
	}{
		{name: "hash prefixed", input: "#42", want: 42, ok: true},
		{name: "bare number", input: "42", want: 42, ok: true},
		{name: "padded", input: "  #7  ", want: 7, ok: true},
		{name: "empty", input: "", ok: false},
		{name: "hash only", input: "#", ok: false},
		{name: "not a number", input: "#abc", ok: false},
		{name: "zero is not an incident", input: "#0", ok: false},
		{name: "negative", input: "-3", ok: false},
		{name: "trailing junk", input: "42x", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			got, ok := telegram.ParseIncidentRef(tt.input)
			r.Equal(tt.ok, ok)

			if tt.ok {
				r.Equal(tt.want, got)
			}
		})
	}
}

// TestParseCallbackData pins the inline-button payload split.
func TestParseCallbackData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		wantAction string
		wantArg    string
	}{
		{
			name:       "ack with uid",
			input:      "ack:2f1b0a1e-0000-4000-8000-000000000001",
			wantAction: "ack",
			wantArg:    "2f1b0a1e-0000-4000-8000-000000000001",
		},
		{name: "uppercase action", input: "ACK:abc", wantAction: "ack", wantArg: "abc"},
		{name: "no argument", input: "ack", wantAction: "ack"},
		{name: "empty", input: ""},
		{name: "colon only", input: ":abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			action, arg := telegram.ParseCallbackData(tt.input)
			r.Equal(tt.wantAction, action)
			r.Equal(tt.wantArg, arg)
		})
	}
}

// TestAckCallbackDataFitsTelegramLimit is the reason the button can carry the
// raw UUID and does not need the short reference: Telegram rejects the whole
// send when callback_data exceeds 64 BYTES.
func TestAckCallbackDataFitsTelegramLimit(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	data := telegram.AckCallbackData("2f1b0a1e-1111-4000-8000-000000000001")
	r.LessOrEqual(len(data), 64)
	r.Equal("ack:2f1b0a1e-1111-4000-8000-000000000001", data)
}

// TestParseUpdate_CallbackQuery proves a button press decodes with everything
// the ack path needs: the query id, the presser, and the chat + message id of
// the alert to rewrite.
func TestParseUpdate_CallbackQuery(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	body := `{"update_id":77,"callback_query":{"id":"cb-1",` +
		`"from":{"id":5,"first_name":"Alice","username":"alice"},` +
		`"message":{"message_id":31,"chat":{"id":-1001,"type":"group"},"text":"alert"},` +
		`"data":"ack:inc-uid"}}`

	update, err := telegram.ParseUpdate([]byte(body))
	r.NoError(err)
	r.NotNil(update.CallbackQuery)
	r.Equal("cb-1", update.CallbackQuery.ID)
	r.Equal("ack:inc-uid", update.CallbackQuery.Data)
	r.Equal("Alice", update.CallbackQuery.From.FirstName)
	r.EqualValues(31, update.CallbackQuery.Message.MessageID)
	r.EqualValues(-1001, update.CallbackQuery.Message.Chat.ID)
}

// TestEmptyInlineKeyboardSerializesAsRemoval pins the one JSON detail that the
// "remove the button" edit depends on: an EMPTY inline_keyboard must survive
// encoding. If it were omitted, Telegram would read the edit as "leave the
// buttons alone" and an acknowledged alert would keep offering Acknowledge.
func TestEmptyInlineKeyboardSerializesAsRemoval(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	encoded, err := json.Marshal(telegram.EmptyInlineKeyboard())
	r.NoError(err)
	r.JSONEq(`{"inline_keyboard":[]}`, string(encoded))
}

// TestAckKeyboard covers the button itself.
func TestAckKeyboard(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Nil(telegram.AckKeyboard("  "), "no incident means no button")

	keyboard := telegram.AckKeyboard("inc-1")
	r.NotNil(keyboard)
	r.Len(keyboard.InlineKeyboard, 1)
	r.Len(keyboard.InlineKeyboard[0], 1)
	r.Equal("ack:inc-1", keyboard.InlineKeyboard[0][0].CallbackData)
	r.Contains(keyboard.InlineKeyboard[0][0].Text, "Acknowledge")
}

// TestBuildAlertHTML_CarriesTheIncidentRef proves the ref reaches the alert
// body — the whole point of the number is that an on-call person can read it
// off the notification and type it straight back.
func TestBuildAlertHTML_CarriesTheIncidentRef(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	body := telegram.BuildAlertHTML(&telegram.AlertParams{
		State:     telegram.StateDown,
		Number:    42,
		CheckName: "API & web",
		OrgSlug:   "acme",
	})

	r.Contains(body, "#42")
	// The escaping contract still holds around the new field.
	r.Contains(body, "API &amp; web")
	r.NotContains(body, "API & web")
}

// TestBuildAlertHTML_OmitsTheRefWhenUnnumbered covers an incident created
// before the numbers existed: "#0" would be a lie, so nothing is rendered.
func TestBuildAlertHTML_OmitsTheRefWhenUnnumbered(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	body := telegram.BuildAlertHTML(&telegram.AlertParams{State: telegram.StateDown, CheckName: "api"})
	r.NotContains(body, "#")
}

// TestBuildAcknowledgedHTML pins the replacement body written over an alert
// once its button is pressed.
func TestBuildAcknowledgedHTML(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	at := time.Date(2026, 8, 13, 9, 5, 0, 0, time.UTC)
	body := telegram.BuildAcknowledgedHTML(
		&telegram.AlertParams{State: telegram.StateDown, Number: 7, CheckName: "api"},
		"alice@example.com", at,
	)

	r.Contains(body, "#7")
	r.Contains(body, "Acknowledged by alice@example.com")
	r.Contains(body, "2026-08-13 09:05 UTC")
	r.Contains(body, "✅")
}

// TestBuildStatusHTML covers both shapes of the one-line health answer.
func TestBuildStatusHTML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		view     telegram.StatusView
		contains []string
	}{
		{
			name:     "all up",
			view:     telegram.StatusView{OrgSlug: "acme", TotalChecks: 45},
			contains: []string{"✅", "acme", "all 45 checks up"},
		},
		{
			name: "incidents open",
			view: telegram.StatusView{
				OrgSlug: "acme", TotalChecks: 45, ChecksDown: 3, OpenIncidents: 3,
			},
			contains: []string{"🔥", "3 incidents open", "42/45 checks up"},
		},
		{
			name: "single incident is singular",
			view: telegram.StatusView{
				OrgSlug: "acme", TotalChecks: 2, ChecksDown: 1, OpenIncidents: 1,
			},
			contains: []string{"1 incident open", "1/2 checks up"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			body := telegram.BuildStatusHTML(&tt.view)

			for _, want := range tt.contains {
				r.Contains(body, want)
			}
		})
	}
}

// TestBuildIncidentLineHTML pins the /incidents row shape.
func TestBuildIncidentLineHTML(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	body := telegram.BuildIncidentLineHTML(&telegram.IncidentLine{
		Number:    42,
		CheckName: "api",
		State:     telegram.StateDown,
		OpenFor:   23 * time.Minute,
	})

	r.Contains(body, "#42")
	r.Contains(body, "api")
	r.Contains(body, "DOWN")
	r.Contains(body, "open 23m")
}

// TestBuildIncidentDetailHTML pins the /incident answer.
func TestBuildIncidentDetailHTML(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	ackedAt := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	body := telegram.BuildIncidentDetailHTML(&telegram.IncidentDetailView{
		Number:    42,
		CheckName: "api",
		State:     telegram.StateEscalated,
		OpenFor:   2 * time.Hour,
		Regions:   []string{"eu2", "", "us1"},
		LastError: "HTTP request failed: 503 Service Unavailable",
		AckedBy:   "alice",
		AckedAt:   &ackedAt,
	})

	r.Contains(body, "#42")
	r.Contains(body, "ESCALATED")
	r.Contains(body, "2h00m")
	r.Contains(body, "eu2, us1")
	r.Contains(body, "503 Service Unavailable")
	r.Contains(body, "alice at 2026-08-13 10:00 UTC")
}

// TestBuildIncidentDetailHTML_UnackedSaysSo makes the absence explicit — a
// missing line reads as "the bot forgot", not "nobody has taken this".
func TestBuildIncidentDetailHTML_UnackedSaysSo(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	body := telegram.BuildIncidentDetailHTML(&telegram.IncidentDetailView{
		Number: 1, CheckName: "api", State: telegram.StateDown, OpenFor: time.Minute,
	})

	r.Contains(body, "not yet")
}

// TestFormatOpenFor covers the compact durations.
func TestFormatOpenFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input time.Duration
		want  string
	}{
		{name: "seconds", input: 12 * time.Second, want: "12s"},
		{name: "minutes", input: 23 * time.Minute, want: "23m"},
		{name: "hours", input: 3*time.Hour + 12*time.Minute, want: "3h12m"},
		{name: "days", input: 50 * time.Hour, want: "2d2h"},
		{name: "negative clamps", input: -time.Hour, want: "0s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, telegram.FormatOpenFor(tt.input))
		})
	}
}

// TestActorLabel pins the group-chat attribution string the incident timeline
// records.
func TestActorLabel(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("via Telegram (Alice)", telegram.ActorLabel("Alice"))
	r.Equal("via Telegram", telegram.ActorLabel("  "))
}

// TestBuildHelpHTML makes sure the advertised commands and the help text agree.
// A command offered in autocomplete that the help never mentions (or vice
// versa) is exactly how a bot ends up promising something it cannot do.
func TestBuildHelpHTML(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	help := telegram.BuildHelpHTML()

	for _, command := range telegram.Commands {
		r.Contains(help, "/"+command.Command, "the help text must mention every advertised command")
	}
}
