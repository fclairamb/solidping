package statussubscribers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/statussubscribers"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

type handlerSetup struct {
	*subSetup
	handler *statussubscribers.Handler
	router  *httpx.Router
	sender  *fakeSender
}

func newHandlerSetup(t *testing.T) *handlerSetup {
	t.Helper()

	ts := newSubSetup(t)
	sender := &fakeSender{}
	cfg := &config.Config{}
	cfg.Server.BaseURL = "https://status.example.com"

	formatter, err := email.NewFormatter()
	require.NoError(t, err)

	handler := statussubscribers.NewHandler(ts.svc, ts.dbSvc, sender, formatter, cfg)

	router := httpx.New()
	router.POST("/api/v1/orgs/:org/status-pages/:statusPageUid/subscribers", handler.Subscribe)
	router.GET("/api/v1/orgs/:org/status-pages/:statusPageUid/subscribers", handler.ListSubscribers)
	router.DELETE("/api/v1/orgs/:org/status-pages/:statusPageUid/subscribers/:uid", handler.RemoveSubscriber)
	router.GET("/api/v1/public/status-subscribers/confirm", handler.Confirm)
	router.GET("/api/v1/public/status-subscribers/unsubscribe", handler.Unsubscribe)
	router.GET("/api/v1/status-pages/:org/:slug/feed.xml", handler.Feed)

	return &handlerSetup{subSetup: ts, handler: handler, router: router, sender: sender}
}

func (h *handlerSetup) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		require.NoError(t, err)
	}

	req := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewBuffer(bodyBytes))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)

	return rec
}

func TestHandlerSubscribeReturns202AndSendsConfirmMail(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHandlerSetup(t)

	rec := h.do(t, http.MethodPost,
		"/api/v1/orgs/"+h.org.Slug+"/status-pages/"+h.page.UID+"/subscribers",
		map[string]any{"email": "widget@example.com"})

	r.Equal(http.StatusAccepted, rec.Code)

	msgs := h.sender.sent()
	r.Len(msgs, 1)
	r.Equal([]string{"widget@example.com"}, msgs[0].Recipients.To)
	r.Contains(msgs[0].HTML, "Confirm")
	r.Contains(msgs[0].HTML, "https://status.example.com/api/v1/public/status-subscribers/confirm")
}

func TestHandlerSubscribeInvalidEmail422(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHandlerSetup(t)

	rec := h.do(t, http.MethodPost,
		"/api/v1/orgs/"+h.org.Slug+"/status-pages/"+h.page.UID+"/subscribers",
		map[string]any{"email": "nope"})

	r.Equal(http.StatusUnprocessableEntity, rec.Code)
	r.Empty(h.sender.sent())
}

func TestHandlerConfirmAndUnsubscribeLanding(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHandlerSetup(t)

	// Subscribe via service to grab tokens.
	res := h.subscribe(t, "land@example.com")

	confirmRec := h.do(t, http.MethodGet,
		"/api/v1/public/status-subscribers/confirm?token="+res.Subscriber.ConfirmToken, nil)
	r.Equal(http.StatusOK, confirmRec.Code)
	r.Contains(confirmRec.Header().Get("Content-Type"), "text/html")
	r.Contains(confirmRec.Body.String(), "confirmed")

	unsubRec := h.do(t, http.MethodGet,
		"/api/v1/public/status-subscribers/unsubscribe?token="+res.Subscriber.UnsubscribeToken, nil)
	r.Equal(http.StatusOK, unsubRec.Code)
	r.Contains(unsubRec.Body.String(), "Unsubscribed")
}

func TestHandlerConfirmBadTokenLanding(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHandlerSetup(t)

	rec := h.do(t, http.MethodGet,
		"/api/v1/public/status-subscribers/confirm?token=bogus", nil)
	r.Equal(http.StatusBadRequest, rec.Code)
	r.Contains(rec.Body.String(), "Link expired")
}

func TestHandlerAdminList(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHandlerSetup(t)

	_ = h.subscribe(t, "a@example.com")
	_ = h.subscribe(t, "b@example.com")

	rec := h.do(t, http.MethodGet,
		"/api/v1/orgs/"+h.org.Slug+"/status-pages/"+h.page.UID+"/subscribers", nil)
	r.Equal(http.StatusOK, rec.Code)

	var resp struct {
		Data []statussubscribers.SubscriberResponse `json:"data"`
		Meta struct {
			Count int `json:"count"`
		} `json:"meta"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Equal(2, resp.Meta.Count)
	r.Len(resp.Data, 2)
}

func TestHandlerFeed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHandlerSetup(t)
	ctx := t.Context()

	// Insert a published status update directly via the DB.
	update := models.NewStatusUpdate(h.org.UID, h.page.UID, "author-uid")
	update.Kind = models.StatusUpdateKindInfo
	update.Title = "Scheduled maintenance"
	update.BodyMarkdown = "We will be performing maintenance."
	// author_uid has an FK to users; create a user.
	user := models.NewUser("feed-author@example.com")
	r.NoError(h.dbSvc.CreateUser(ctx, user))
	update.AuthorUID = user.UID
	r.NoError(h.dbSvc.CreateStatusUpdate(ctx, update))

	rec := h.do(t, http.MethodGet,
		"/api/v1/status-pages/"+h.org.Slug+"/"+h.page.Slug+"/feed.xml", nil)
	r.Equal(http.StatusOK, rec.Code)
	r.Contains(rec.Header().Get("Content-Type"), "application/atom+xml")
	r.Contains(rec.Header().Get("Cache-Control"), "max-age")

	body := rec.Body.String()
	r.Contains(body, "<feed")
	r.Contains(body, "Scheduled maintenance")
	r.Contains(body, "Acme Status status updates")
}

func TestHandlerFeedUnknownPage404(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHandlerSetup(t)

	rec := h.do(t, http.MethodGet,
		"/api/v1/status-pages/"+h.org.Slug+"/no-such-page/feed.xml", nil)
	r.Equal(http.StatusNotFound, rec.Code)
}
