package msteams

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// newTestClient builds a client wired to a fake Bot Connector. Each test uses
// a distinct app id so the process-wide token cache cannot leak a token
// between parallel tests.
func newTestClient(t *testing.T, connector *fakeConnector, appID string) *Client {
	t.Helper()

	return NewClientWithTokenURL(appID, testAppSecret, connector.server.URL, connector.tokenURL())
}

func TestClient_SendToConversation(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	connector := newFakeConnector(t)
	client := newTestClient(t, connector, "app-send")

	result, err := client.SendToConversation(t.Context(), "19:channel-a", NewTextMessage("hello"))
	r.NoError(err)
	r.NotEmpty(result.ID)

	calls := connector.recorded()
	r.Len(calls, 1)
	r.Equal(http.MethodPost, calls[0].Method)
	r.Equal("/v3/conversations/19:channel-a/activities", calls[0].Path)
	r.Equal("hello", calls[0].Activity.Text)
}

func TestClient_ReplyToActivitySetsReplyToID(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	connector := newFakeConnector(t)
	client := newTestClient(t, connector, "app-reply")

	_, err := client.ReplyToActivity(t.Context(), "19:channel-a", "act-1", NewTextMessage("in thread"))
	r.NoError(err)

	calls := connector.recorded()
	r.Len(calls, 1)
	r.Equal(http.MethodPost, calls[0].Method)
	r.Equal("/v3/conversations/19:channel-a/activities/act-1", calls[0].Path)
	// replyToId is what makes Teams render the message as a threaded reply.
	r.Equal("act-1", calls[0].Activity.ReplyToID)
}

func TestClient_UpdateActivityUsesPut(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	connector := newFakeConnector(t)
	client := newTestClient(t, connector, "app-update")

	_, err := client.UpdateActivity(t.Context(), "19:channel-a", "act-1", NewTextMessage("updated"))
	r.NoError(err)

	calls := connector.recorded()
	r.Len(calls, 1)
	// PUT (not POST) is the in-place update — a POST here would add a second
	// message to the channel instead of rewriting the card.
	r.Equal(http.MethodPut, calls[0].Method)
	r.Equal("/v3/conversations/19:channel-a/activities/act-1", calls[0].Path)
	r.Equal("act-1", calls[0].Activity.ID)
}

// TestClient_TokenIsCachedAcrossCalls pins that a burst of notifications does
// not mint one bearer token per message.
func TestClient_TokenIsCachedAcrossCalls(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	connector := newFakeConnector(t)
	client := newTestClient(t, connector, "app-token-cache")

	for range 3 {
		_, err := client.SendToConversation(t.Context(), "19:channel-a", NewTextMessage("x"))
		r.NoError(err)
	}

	connector.mu.Lock()
	hits := connector.tokenHit
	connector.mu.Unlock()

	r.Equal(1, hits)
}

func TestClient_SurfacesConnectorErrors(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	connector := newFakeConnector(t)
	client := newTestClient(t, connector, "app-error")

	connector.failNext(http.StatusForbidden)

	_, err := client.SendToConversation(t.Context(), "19:channel-a", NewTextMessage("x"))
	r.ErrorIs(err, ErrBotConnector)
	r.Contains(err.Error(), "403")
}

func TestClient_RequiresCredentials(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	connector := newFakeConnector(t)
	client := NewClientWithTokenURL("", "", connector.server.URL, connector.tokenURL())

	_, err := client.SendToConversation(t.Context(), "19:channel-a", NewTextMessage("x"))
	r.ErrorIs(err, ErrMissingCredentials)
}

func TestClient_RequiresServiceURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	connector := newFakeConnector(t)
	client := NewClientWithTokenURL("app-nosvc", testAppSecret, "", connector.tokenURL())

	_, err := client.SendToConversation(t.Context(), "19:channel-a", NewTextMessage("x"))
	r.ErrorIs(err, ErrMissingServiceURL)
}

// TestClient_SurfacesTokenFailure covers a rejected client-credentials
// exchange (e.g. a rotated app secret).
func TestClient_SurfacesTokenFailure(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	connector := newFakeConnector(t)
	client := NewClientWithTokenURL(
		"app-badsecret", testAppSecret, connector.server.URL, connector.server.URL+"/no-such-token-endpoint",
	)

	_, err := client.SendToConversation(t.Context(), "19:channel-a", NewTextMessage("x"))
	r.ErrorIs(err, ErrTokenRequest)
}
