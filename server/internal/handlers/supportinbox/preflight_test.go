package supportinbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/supportinbox"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/support"
)

// errProviderRejected is a send-time provider failure — err113 forbids inline
// dynamic errors, tests included.
var errProviderRejected = errors.New("provider rejected the message")

// preflightHarness drives the REAL handler methods over HTTP, minus the
// super-admin gate (which TestSupportRoutesRequireSuperAdmin covers on its own).
type preflightHarness struct {
	server *httptest.Server
	inbox  *support.Service
}

func newPreflightHarness(t *testing.T, inbox *support.Service) *preflightHarness {
	t.Helper()

	handler := supportinbox.NewHandler(inbox, &config.Config{})

	router := httpx.New()
	group := router.NewGroup("/api/v1/support")
	group.GET("/threads", handler.ListThreads)
	group.GET("/threads/:uid", handler.GetThread)
	group.GET("/threads/:uid/messages", handler.ListMessages)
	group.POST("/threads/:uid/messages", handler.CreateMessage)
	group.POST("/threads/:uid/messages/:messageUid/resend", handler.ResendMessage)

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &preflightHarness{server: server, inbox: inbox}
}

func (h *preflightHarness) call(t *testing.T, method, path, body string) (int, []byte) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, h.server.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	payload, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, payload
}

// storedMessages counts what actually landed in the store, which is the
// assertion a "nothing was recorded" claim lives or dies on.
func (h *preflightHarness) storedMessages(t *testing.T, threadUID string) int {
	t.Helper()

	messages, err := h.inbox.ListMessages(t.Context(), threadUID, 0)
	require.NoError(t, err)

	return len(messages)
}

// TestCreateMessage_UnroutableThreadIs409AndStoresNothing is the HTTP-level
// half of the refusal: a stale dashboard tab or a scripted caller must not be
// able to store an outbound message that had no route.
func TestCreateMessage_UnroutableThreadIs409AndStoresNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	inbox := support.NewService(dbSvc, support.Options{})

	sends := 0
	inbox.RegisterRoutedReplier(models.SupportChannelSlack,
		func(_ context.Context, _ *models.SupportThread, _ string) (string, error) {
			sends++

			return "1724500000.000100", nil
		},
		func(_ context.Context, thread *models.SupportThread) support.ReplyRoute {
			if teamID, _ := thread.ChannelContext["teamId"].(string); teamID != "T0ACME0001" {
				return support.ReplyRoute{
					Reason: "SolidPing holds no bot token for this Slack workspace",
				}
			}

			return support.ReplyRoute{CanReply: true}
		})

	orphan, _, err := inbox.Capture(ctx, &support.Inbound{
		Channel: models.SupportChannelSlack, Identity: "U0ACME9999",
		ExternalID: "slack:api:orphan", Body: "hi",
		Context: map[string]any{"teamId": "T0ACME0002", "channelId": "D0ACME2222"},
	})
	r.NoError(err)

	routable, _, err := inbox.Capture(ctx, &support.Inbound{
		Channel: models.SupportChannelSlack, Identity: "U0ACME1234",
		ExternalID: "slack:api:ok", Body: "hi",
		Context: map[string]any{"teamId": "T0ACME0001", "channelId": "D0ACME1111"},
	})
	r.NoError(err)

	harness := newPreflightHarness(t, inbox)

	// The listing tells the dashboard which thread is answerable, per thread.
	status, payload := harness.call(t, http.MethodGet, "/api/v1/support/threads", "")
	r.Equal(http.StatusOK, status)

	var listed struct {
		Data []struct {
			UID            string `json:"uid"`
			CanReply       bool   `json:"canReply"`
			CanReplyReason string `json:"canReplyReason"`
		} `json:"data"`
	}

	r.NoError(json.Unmarshal(payload, &listed))
	r.Len(listed.Data, 2)

	byUID := map[string]struct {
		can    bool
		reason string
	}{}
	for _, row := range listed.Data {
		byUID[row.UID] = struct {
			can    bool
			reason string
		}{row.CanReply, row.CanReplyReason}
	}

	r.False(byUID[orphan.UID].can)
	r.Contains(byUID[orphan.UID].reason, "no bot token")
	r.True(byUID[routable.UID].can, "positive control: the connected workspace stays answerable")
	r.Empty(byUID[routable.UID].reason)

	// The refusal: 409, with the reason, and NOTHING stored.
	status, payload = harness.call(t, http.MethodPost,
		"/api/v1/support/threads/"+orphan.UID+"/messages", `{"body":"we are on it"}`)
	r.Equal(http.StatusConflict, status)
	r.Contains(string(payload), "no bot token")
	r.Zero(sends)
	r.Equal(1, harness.storedMessages(t, orphan.UID),
		"a 409 must leave only the inbound message behind")

	// POSITIVE CONTROL over the same endpoint.
	status, _ = harness.call(t, http.MethodPost,
		"/api/v1/support/threads/"+routable.UID+"/messages", `{"body":"we are on it"}`)
	r.Equal(http.StatusCreated, status)
	r.Equal(1, sends)
	r.Equal(2, harness.storedMessages(t, routable.UID))
}

// TestResendMessage_RetriesAFailedReply covers the endpoint that gives an
// operator a way out of stored-but-unsent text.
func TestResendMessage_RetriesAFailedReply(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	inbox := support.NewService(dbSvc, support.Options{})

	providerUp := false
	inbox.RegisterReplier(models.SupportChannelTelegram,
		func(_ context.Context, _ *models.SupportThread, _ string) (string, error) {
			if !providerUp {
				return "", errProviderRejected
			}

			return "4242", nil
		})

	thread, _, err := inbox.Capture(ctx, &support.Inbound{
		Channel: models.SupportChannelTelegram, Identity: "999",
		ExternalID: "tg:resend:1", Body: "hi",
	})
	r.NoError(err)

	harness := newPreflightHarness(t, inbox)

	// A genuine send failure: attempted, rejected, and therefore RECORDED — the
	// behaviour the pre-flight must not sweep away.
	status, payload := harness.call(t, http.MethodPost,
		"/api/v1/support/threads/"+thread.UID+"/messages", `{"body":"we are on it"}`)
	r.Equal(http.StatusAccepted, status)

	var failed struct {
		UID      string         `json:"uid"`
		Delivery map[string]any `json:"delivery"`
	}

	r.NoError(json.Unmarshal(payload, &failed))
	r.Equal("failed", failed.Delivery["status"])

	// Retried while the provider is still down: attempted again, so 202 and the
	// row keeps its failed flag with the fresh error.
	status, _ = harness.call(t, http.MethodPost,
		"/api/v1/support/threads/"+thread.UID+"/messages/"+failed.UID+"/resend", "")
	r.Equal(http.StatusAccepted, status)
	r.Equal(2, harness.storedMessages(t, thread.UID), "a resend must never append a duplicate")

	// Provider back: the same row goes out and flips to sent.
	providerUp = true

	status, payload = harness.call(t, http.MethodPost,
		"/api/v1/support/threads/"+thread.UID+"/messages/"+failed.UID+"/resend", "")
	r.Equal(http.StatusOK, status)
	r.Contains(string(payload), `"status":"sent"`)
	r.Equal(2, harness.storedMessages(t, thread.UID))

	// Resending a delivered reply would send the customer the same text twice.
	status, _ = harness.call(t, http.MethodPost,
		"/api/v1/support/threads/"+thread.UID+"/messages/"+failed.UID+"/resend", "")
	r.Equal(http.StatusConflict, status)

	// An unknown message uid is a 404, not a 500.
	status, _ = harness.call(t, http.MethodPost,
		"/api/v1/support/threads/"+thread.UID+"/messages/00000000-0000-0000-0000-000000000000/resend", "")
	r.Equal(http.StatusNotFound, status)
}
