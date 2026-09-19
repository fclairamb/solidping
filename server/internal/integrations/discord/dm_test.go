package discord

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// dmFake stands in for Discord's REST API on the DM routes.
type dmFake struct {
	server *httptest.Server

	mu sync.Mutex
	// calls records "METHOD path" in order.
	calls []string
	// dmChannelID is what POST /users/@me/channels returns.
	dmChannelID string
	// postStatus / postBody answer POST /channels/{id}/messages. A per-channel
	// override models "this cached channel is gone".
	postStatus   map[string]int
	postBody     string
	defaultPostS int
}

func newDMFake(t *testing.T) *dmFake {
	t.Helper()

	fake := &dmFake{
		dmChannelID:  "DM-1",
		postStatus:   map[string]int{},
		postBody:     `{"id":"M-1","channel_id":"DM-1"}`,
		defaultPostS: http.StatusOK,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/users/@me/channels", func(w http.ResponseWriter, req *http.Request) {
		fake.record(req.Method + " " + req.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + fake.dmChannelID + `","type":1}`))
	})
	mux.HandleFunc("/channels/", func(w http.ResponseWriter, req *http.Request) {
		fake.record(req.Method + " " + req.URL.Path)

		status := fake.defaultPostS

		fake.mu.Lock()
		for channel, override := range fake.postStatus {
			if channel != "" && req.URL.Path == "/channels/"+channel+"/messages" {
				status = override
			}
		}
		fake.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)

		if status == http.StatusOK {
			_, _ = w.Write([]byte(fake.postBody))

			return
		}

		_, _ = w.Write([]byte(`{"code":10003,"message":"Unknown Channel"}`))
	})

	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *dmFake) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, call)
}

func (f *dmFake) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.calls...)
}

func (f *dmFake) client() *BotClient {
	return NewBotClient("bot-token").WithBaseURL(f.server.URL)
}

// memStore is a ContactDMStore that records what was cached.
type memStore struct {
	writes []string
}

func (m *memStore) SetUserContactDMChannel(_ context.Context, uid, channelID string) error {
	m.writes = append(m.writes, uid+"="+channelID)

	return nil
}

func discordContact(dmChannel string) *models.UserContact {
	contact := models.NewUserContact(
		"user-1", "org-1", models.UserContactTypeDiscord, "SNOWFLAKE", "Discord")
	contact.UID = "contact-1"

	if dmChannel != "" {
		value := dmChannel
		contact.DMChannelID = &value
	}

	return contact
}

// TestCreateDMPostsRecipientID pins the request CreateDM makes: Discord expects
// a `recipient_id` on POST /users/@me/channels and answers with a type-1 channel.
func TestCreateDMPostsRecipientID(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var body []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.Equal(http.MethodPost, req.Method)
		r.Equal("/users/@me/channels", req.URL.Path)
		r.Equal("Bot bot-token", req.Header.Get("Authorization"))

		body, _ = io.ReadAll(req.Body)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"DM-9","type":1}`))
	}))
	defer server.Close()

	channel, err := NewBotClient("bot-token").WithBaseURL(server.URL).
		CreateDM(t.Context(), "SNOWFLAKE")
	r.NoError(err)
	r.Equal("DM-9", channel.ID)
	r.Equal(ChannelTypeDM, channel.Type)
	r.JSONEq(`{"recipient_id":"SNOWFLAKE"}`, string(body))
}

// TestCreateDMRejectsEmptyRecipient: a blank snowflake must not reach Discord as
// a malformed request whose 400 says nothing about the cause.
func TestCreateDMRejectsEmptyRecipient(t *testing.T) {
	t.Parallel()

	_, err := NewBotClient("bot-token").CreateDM(t.Context(), "")
	require.ErrorIs(t, err, ErrEmptyRecipient)
}

// TestAPIErrorDecodesDiscordCode: the error envelope used to be thrown away, and
// substring matching was the only way to tell a recipient refusal from an outage.
// The decoded code is what the "unavailable, not failed" policy branches on.
func TestAPIErrorDecodesDiscordCode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		status       int
		body         string
		wantCode     int
		cannotDMUser bool
	}{
		{
			name:         "50007 is recognised as a recipient refusal",
			status:       http.StatusForbidden,
			body:         `{"code":50007,"message":"Cannot send messages to this user"}`,
			wantCode:     50007,
			cannotDMUser: true,
		},
		{
			name:     "another 403 code is not a recipient refusal",
			status:   http.StatusForbidden,
			body:     `{"code":50013,"message":"Missing Permissions"}`,
			wantCode: 50013,
		},
		{
			name:     "a body with no envelope still yields an APIError, code 0",
			status:   http.StatusInternalServerError,
			body:     `<html>bad gateway</html>`,
			wantCode: 0,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(testCase.body))
			}))
			defer server.Close()

			_, err := NewBotClient("bot-token").WithBaseURL(server.URL).
				CreateDM(t.Context(), "SNOWFLAKE")
			r.Error(err)

			var apiErr *APIError
			r.ErrorAs(err, &apiErr)
			r.Equal(testCase.status, apiErr.Status)
			r.Equal(testCase.wantCode, apiErr.Code)
			r.Equal(testCase.cannotDMUser, IsCannotDMUser(err))

			// Every pre-existing caller checks ErrUnexpectedStatus; the typed
			// error must not have broken them.
			r.ErrorIs(err, ErrUnexpectedStatus)
		})
	}
}

// TestSendContactDMOpensCachesAndReuses: the first send opens the DM and caches
// it, the second reuses the cache. That saved round trip is the only reason the
// dm_channel_id column exists.
func TestSendContactDMOpensCachesAndReuses(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newDMFake(t)
	store := &memStore{}
	contact := discordContact("")

	_, err := SendContactDM(t.Context(), fake.client(), store, contact, &Message{Content: "first"})
	r.NoError(err)

	_, err = SendContactDM(t.Context(), fake.client(), store, contact, &Message{Content: "second"})
	r.NoError(err)

	r.Equal([]string{
		"POST /users/@me/channels",
		"POST /channels/DM-1/messages",
		"POST /channels/DM-1/messages",
	}, fake.recorded(), "the DM must be opened once and then reused")
	r.Equal([]string{"contact-1=DM-1"}, store.writes)
}

// TestSendContactDMReopensAfterStaleCache: a cached channel Discord has since
// 404'd must be forgotten and re-opened, not left as a permanently un-pageable
// route. Restored backups and deleted accounts produce exactly this.
func TestSendContactDMReopensAfterStaleCache(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newDMFake(t)
	fake.postStatus["DM-STALE"] = http.StatusNotFound
	store := &memStore{}
	contact := discordContact("DM-STALE")

	result, err := SendContactDM(t.Context(), fake.client(), store, contact, &Message{Content: "page"})
	r.NoError(err)
	r.Equal("M-1", result.ID)

	r.Equal([]string{
		"POST /channels/DM-STALE/messages",
		"POST /users/@me/channels",
		"POST /channels/DM-1/messages",
	}, fake.recorded())
	// The stale id is cleared BEFORE the re-open, so a second failure cannot
	// leave a known-bad id on the row.
	r.Equal([]string{"contact-1=", "contact-1=DM-1"}, store.writes)
}

// TestSendContactDMRejectsWrongContactType: silently DMing whatever string
// happened to be in Value is how a Slack user id would end up addressed to
// Discord.
func TestSendContactDMRejectsWrongContactType(t *testing.T) {
	t.Parallel()

	fake := newDMFake(t)
	contact := models.NewUserContact("u", "o", models.UserContactTypeSlackUser, "U123", "Slack")

	_, err := SendContactDM(t.Context(), fake.client(), nil, contact, &Message{Content: "x"})
	require.ErrorIs(t, err, ErrNotDiscordContact)
	require.Empty(t, fake.recorded(), "nothing may reach Discord for a non-discord contact")
}

// TestSendContactDM50007SurfacesUnchanged: the refusal must reach the caller as a
// recognisable 50007 so IT can decide the policy. Swallowing or rewrapping it
// here is how paging coverage would stop falling through to the next route.
func TestSendContactDM50007SurfacesUnchanged(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/users/@me/channels" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"DM-1","type":1}`))

			return
		}

		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":50007,"message":"Cannot send messages to this user"}`))
	}))
	defer server.Close()

	client := NewBotClient("bot-token").WithBaseURL(server.URL)

	_, err := SendContactDM(t.Context(), client, &memStore{}, discordContact(""), &Message{Content: "x"})
	r.Error(err)
	r.True(IsCannotDMUser(err))
}

// TestSupportsThreadsRejectsDMChannel is the one-line invariant the sender's
// no-thread branch rests on.
func TestSupportsThreadsRejectsDMChannel(t *testing.T) {
	t.Parallel()

	require.False(t, SupportsThreads(ChannelTypeDM))
	require.True(t, SupportsThreads(ChannelTypeGuildText))
	require.True(t, SupportsThreads(ChannelTypePublicThread))
}
