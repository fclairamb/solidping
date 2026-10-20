package notifications

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

const (
	// oversizedBody is what the hostile server tries to send: far more than
	// any cap, so an uncapped read shows up clearly in the byte count.
	oversizedBody = 64 << 20
	// transportMargin covers what kernel socket buffers and the client's
	// bufio reader absorb before the client closes the connection.
	transportMargin = 8 << 20
	// errorPrefixMargin covers the sentinel and status text around the
	// snippet in an error string.
	errorPrefixMargin = 256
)

// streamingServer answers every request with status, then streams up to size
// bytes until the client stops reading. written reports the bytes the server
// managed to hand to the connection; done closes when the handler returns.
func streamingServer(t *testing.T, status, size int) (string, *atomic.Int64, chan struct{}) {
	t.Helper()

	var written atomic.Int64

	done := make(chan struct{}, 8)
	chunk := bytes.Repeat([]byte("x"), 32<<10)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defer func() { done <- struct{}{} }()

		w.WriteHeader(status)

		for written.Load() < int64(size) {
			n, err := w.Write(chunk)
			written.Add(int64(n))

			if err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	return srv.URL, &written, done
}

type bodyCapCase struct {
	name string
	// oversizeStatus is the status the hostile server answers with: an error
	// status for most senders, 200 for those that parse a success answer.
	oversizeStatus int
	// successBody, when set, is a normal success answer that must still parse.
	successBody string
	send        func(url string) error
}

func bodyCapCases() []bodyCapCase {
	ctx := context.Background()

	matrixSettings := func(url, room string) models.JSONMap {
		return models.JSONMap{"homeserverUrl": url, "accessToken": "syt_token", "roomId": room}
	}

	return []bodyCapCase{
		{name: "googlechat", oversizeStatus: http.StatusInternalServerError, send: func(url string) error {
			return (&GoogleChatSender{}).Send(ctx, newJobCtx(),
				googleChatPayload(models.JSONMap{"webhook_url": url + "/v1/spaces/A/messages?key=k"}))
		}},
		{name: "gotify", oversizeStatus: http.StatusInternalServerError, send: func(url string) error {
			return (&GotifySender{}).Send(ctx, newJobCtx(), gotifyPayload(eventTypeIncidentCreated,
				models.JSONMap{"server_url": url, "app_token": "tok"}))
		}},
		{
			name: "matrix send", oversizeStatus: http.StatusOK, successBody: `{"event_id":"$abc"}`,
			send: func(url string) error {
				return (&MatrixSender{}).Send(ctx, newJobCtx(),
					matrixTestPayload(eventTypeIncidentCreated, matrixSettings(url, "!room:acme.com")))
			},
		},
		{
			name: "matrix alias", oversizeStatus: http.StatusOK,
			successBody: `{"room_id":"!room:acme.com","event_id":"$abc"}`,
			send: func(url string) error {
				return (&MatrixSender{}).Send(ctx, newJobCtx(),
					matrixTestPayload(eventTypeIncidentCreated, matrixSettings(url, "#alerts:acme.com")))
			},
		},
		{name: "mattermost", oversizeStatus: http.StatusInternalServerError, send: func(url string) error {
			return (&MattermostSender{}).Send(ctx, newJobCtx(), mattermostPayload(models.JSONMap{"webhook_url": url}))
		}},
		{name: "msteams", oversizeStatus: http.StatusInternalServerError, send: func(url string) error {
			return (&MSTeamsSender{}).Send(ctx, newJobCtx(), msTeamsPayload(eventTypeIncidentCreated, url))
		}},
		{name: "ntfy", oversizeStatus: http.StatusInternalServerError, send: func(url string) error {
			payload := pagerdutyPayload(eventTypeIncidentCreated)
			payload.Integration = &models.Integration{
				UID: "chan-ntfy", Type: models.ConnectionTypeNtfy,
				Settings: models.JSONMap{"server_url": url, "topic": "alerts"},
			}

			return (&NtfySender{}).Send(ctx, newJobCtx(), payload)
		}},
		{name: "slackwebhook", oversizeStatus: http.StatusInternalServerError, send: func(url string) error {
			return (&SlackWebhookSender{}).Send(ctx, newJobCtx(),
				slackWebhookPayload(eventTypeIncidentCreated, models.JSONMap{"webhook_url": url}))
		}},
		{
			name: "zulip", oversizeStatus: http.StatusOK, successBody: `{"result":"success","msg":""}`,
			send: func(url string) error {
				return (&ZulipSender{}).Send(ctx, newJobCtx(),
					zulipPayload(eventTypeIncidentCreated, 1, zulipSettingsMap(url)))
			},
		},
		{
			name: "pushover", oversizeStatus: http.StatusOK, successBody: `{"status":1}`,
			send: func(url string) error {
				return (&PushoverSender{APIURL: url}).Send(ctx, newJobCtx(),
					pushoverPayload(models.JSONMap{"user_key": "u-1", "api_token": "a-1"}))
			},
		},
		{name: "pagerduty", oversizeStatus: http.StatusBadRequest, send: func(url string) error {
			return (&PagerDutySender{EventsURL: url}).Send(ctx, newJobCtx(), pagerdutyPayload(eventTypeIncidentCreated))
		}},
	}
}

// TestSenders_CapOversizedResponse: a host that streams an endless body
// makes the sender fail fast with a short error, instead of buffering the
// whole stream until the timeout (spec 2026-10-20-01).
func TestSenders_CapOversizedResponse(t *testing.T) {
	t.Parallel()

	for _, tc := range bodyCapCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			url, written, done := streamingServer(t, tc.oversizeStatus, oversizedBody)

			err := tc.send(url)
			r.Error(err)
			r.LessOrEqual(len(err.Error()), senderErrorSnippet+errorPrefixMargin, "error must carry a short snippet")

			select {
			case <-done:
			case <-time.After(10 * time.Second):
				r.Fail("the server kept writing after the client stopped reading")
			}

			r.Less(written.Load(), int64(senderMaxResponseBody+transportMargin),
				"the client read far past the cap")
		})
	}
}

// TestSenders_ShortErrorKeepsMessage is the positive control: a normal short
// error answer still surfaces the server's message.
func TestSenders_ShortErrorKeepsMessage(t *testing.T) {
	t.Parallel()

	for _, tc := range bodyCapCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"boom-marker"}`))
			}))
			t.Cleanup(srv.Close)

			err := tc.send(srv.URL)
			r.Error(err)
			r.Contains(err.Error(), "boom-marker")
		})
	}
}

// TestSenders_SmallSuccessStillParses: senders that parse a success answer
// still accept a normal one under the cap.
func TestSenders_SmallSuccessStillParses(t *testing.T) {
	t.Parallel()

	for _, tc := range bodyCapCases() {
		if tc.successBody == "" {
			continue
		}

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.successBody))
			}))
			t.Cleanup(srv.Close)

			r.NoError(tc.send(srv.URL))
		})
	}
}

func TestReadCappedBody(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	data, truncated := readCappedBody(bytes.NewReader([]byte("12345")), 5)
	r.Equal("12345", string(data))
	r.False(truncated)

	data, truncated = readCappedBody(bytes.NewReader([]byte("123456")), 5)
	r.Equal("12345", string(data))
	r.True(truncated)

	r.Len(bodySnippet(bytes.Repeat([]byte("a"), senderErrorSnippet)), senderErrorSnippet)
	r.Contains(bodySnippet(bytes.Repeat([]byte("a"), senderErrorSnippet+1)), "[truncated]")
}
