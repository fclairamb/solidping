package notifications

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// matrixTestPayload builds a Payload for the Matrix sender tests, mirroring
// webhook_test.go's testPayload but with a ConnectionTypeMatrix integration
// and a caller-chosen event type.
func matrixTestPayload(eventType string, connSettings models.JSONMap) *Payload {
	name := "API health"
	title := "api.example.com -> HTTP 500"

	return &Payload{
		EventType: eventType,
		Incident: &models.Incident{
			UID:          "018e4a2b-incident",
			StartedAt:    time.Now().Add(-2 * time.Minute),
			FailureCount: 3,
			Title:        &title,
		},
		Check: &models.Check{
			UID:  "018e4a1c-check",
			Name: &name,
			Type: "http",
		},
		Integration: &models.Integration{
			UID:             "chan-1",
			OrganizationUID: "org-1",
			Type:            models.ConnectionTypeMatrix,
			Settings:        connSettings,
		},
		OrgSlug:    "acme",
		AppBaseURL: "https://solidping.example.com",
	}
}

// TestMatrixSender_Send_HitsCorrectURLMethodHeadersBody asserts a plain
// (non-aliased) send PUTs to the right Client-Server API path, carries the
// bearer token, and marshals the expected m.room.message event.
func TestMatrixSender_Send_HitsCorrectURLMethodHeadersBody(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var (
		gotMethod string
		gotPath   string
		gotAuth   string
		gotBody   []byte
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotMethod = req.Method
		// EscapedPath preserves the percent-encoding the client sent, unlike
		// the auto-decoded req.URL.Path.
		gotPath = req.URL.EscapedPath()
		gotAuth = req.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(req.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"event_id":"$abc"}`))
	}))
	defer srv.Close()

	payload := matrixTestPayload(eventTypeIncidentCreated, models.JSONMap{
		"homeserverUrl": srv.URL,
		"accessToken":   "syt_secret_token",
		"roomId":        "!room:matrix.org",
	})

	sender := &MatrixSender{}
	r.NoError(sender.Send(context.Background(), newJobCtx(), payload))

	r.Equal(http.MethodPut, gotMethod)
	r.True(strings.HasPrefix(gotPath, "/_matrix/client/v3/rooms/%21room:matrix.org/send/m.room.message/"))
	r.Equal("Bearer syt_secret_token", gotAuth)

	var event matrixMessageEvent
	r.NoError(json.Unmarshal(gotBody, &event))
	r.Equal("m.text", event.MsgType)
	r.Equal("org.matrix.custom.html", event.Format)
	r.Contains(event.Body, "[DOWN] API health")
	r.Contains(event.FormattedBody, "<strong>[DOWN] API health</strong>")
}

// TestMatrixSender_Send_TxnIDUniquePerSend verifies two sends never reuse a
// txnId — required by the Matrix API (txnId must be unique per access token)
// and load-bearing for retries: a reused txnId would make a retried send a
// silent no-op on the homeserver side.
func TestMatrixSender_Send_TxnIDUniquePerSend(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var txnIDs []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		parts := strings.Split(req.URL.Path, "/")
		txnIDs = append(txnIDs, parts[len(parts)-1])
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	payload := matrixTestPayload(eventTypeIncidentCreated, models.JSONMap{
		"homeserverUrl": srv.URL,
		"accessToken":   "syt_secret_token",
		"roomId":        "!room:matrix.org",
	})

	sender := &MatrixSender{}
	r.NoError(sender.Send(context.Background(), newJobCtx(), payload))
	r.NoError(sender.Send(context.Background(), newJobCtx(), payload))

	r.Len(txnIDs, 2)
	r.NotEmpty(txnIDs[0])
	r.NotEmpty(txnIDs[1])
	r.NotEqual(txnIDs[0], txnIDs[1], "each send must use a fresh txnId")
}

// TestMatrixSender_Send_TrailingSlashTolerated asserts a homeserverUrl with a
// trailing slash does not produce a double slash in the request path.
func TestMatrixSender_Send_TrailingSlashTolerated(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotPath = req.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	payload := matrixTestPayload(eventTypeIncidentCreated, models.JSONMap{
		"homeserverUrl": srv.URL + "/",
		"accessToken":   "syt_secret_token",
		"roomId":        "!room:matrix.org",
	})

	sender := &MatrixSender{}
	r.NoError(sender.Send(context.Background(), newJobCtx(), payload))

	r.False(strings.Contains(gotPath, "//_matrix"), "trailing slash must not produce a double slash: %s", gotPath)
	r.True(strings.HasPrefix(gotPath, "/_matrix/client/v3/rooms/"))
}

// TestMatrixSender_Send_AliasResolution verifies a "#"-prefixed room id is
// resolved via the directory API before sending, and the send targets the
// resolved "!" room id rather than the alias.
func TestMatrixSender_Send_AliasResolution(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// The handlers only capture request data — never assert with `require`
	// inside an httptest handler goroutine: a failed require.* calls
	// t.FailNow(), which Goexits the handler goroutine without writing a
	// response and makes the client see a bare connection-closed/EOF instead
	// of the real assertion failure.
	var (
		resolveHit    bool
		resolveMethod string
		resolvePath   string
		sendPath      string
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/_matrix/client/v3/directory/room/", func(w http.ResponseWriter, req *http.Request) {
		resolveHit = true
		resolveMethod = req.Method
		resolvePath = req.URL.EscapedPath()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"room_id":"!resolved:matrix.org","servers":["matrix.org"]}`))
	})
	mux.HandleFunc("/_matrix/client/v3/rooms/", func(w http.ResponseWriter, req *http.Request) {
		sendPath = req.URL.EscapedPath()
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	payload := matrixTestPayload(eventTypeIncidentCreated, models.JSONMap{
		"homeserverUrl": srv.URL,
		"accessToken":   "syt_secret_token",
		"roomId":        "#room:matrix.org",
	})

	sender := &MatrixSender{}
	r.NoError(sender.Send(context.Background(), newJobCtx(), payload))

	r.True(resolveHit, "alias must be resolved via the directory API")
	r.Equal(http.MethodGet, resolveMethod)
	// The alias must be URL-encoded in the path.
	r.Contains(resolvePath, "%23room:matrix.org")
	r.True(strings.HasPrefix(sendPath, "/_matrix/client/v3/rooms/%21resolved:matrix.org/send/"),
		"send must target the resolved room id, not the alias: %s", sendPath)
}

// TestMatrixSender_Send_AliasResolvedOnEverySend verifies there is no
// caching: two sends against the same alias each perform their own directory
// lookup.
func TestMatrixSender_Send_AliasResolvedOnEverySend(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	resolveHits := 0

	mux := http.NewServeMux()
	mux.HandleFunc("/_matrix/client/v3/directory/room/", func(w http.ResponseWriter, _ *http.Request) {
		resolveHits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"room_id":"!resolved:matrix.org"}`))
	})
	mux.HandleFunc("/_matrix/client/v3/rooms/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	payload := matrixTestPayload(eventTypeIncidentCreated, models.JSONMap{
		"homeserverUrl": srv.URL,
		"accessToken":   "syt_secret_token",
		"roomId":        "#room:matrix.org",
	})

	sender := &MatrixSender{}
	r.NoError(sender.Send(context.Background(), newJobCtx(), payload))
	r.NoError(sender.Send(context.Background(), newJobCtx(), payload))

	r.Equal(2, resolveHits, "each send must re-resolve the alias, no caching")
}

// TestMatrixSender_Send_HTMLEscaping verifies dynamic values (check name,
// failure reason) are escaped in formatted_body and cannot inject markup.
func TestMatrixSender_Send_HTMLEscaping(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotBody, _ = io.ReadAll(req.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	maliciousName := `<script>alert(1)</script>`
	payload := matrixTestPayload(eventTypeIncidentCreated, models.JSONMap{
		"homeserverUrl": srv.URL,
		"accessToken":   "syt_secret_token",
		"roomId":        "!room:matrix.org",
	})
	payload.Check.Name = &maliciousName

	sender := &MatrixSender{}
	r.NoError(sender.Send(context.Background(), newJobCtx(), payload))

	var event matrixMessageEvent
	r.NoError(json.Unmarshal(gotBody, &event))

	r.NotContains(event.FormattedBody, "<script>", "raw markup must never appear in formatted_body")
	r.Contains(event.FormattedBody, "&lt;script&gt;")
	// The plain body carries the raw text — no markup interpretation happens there.
	r.Contains(event.Body, maliciousName)
}

// TestMatrixSender_Send_EventFormatting pins the title prefix per event type,
// mirroring ntfy's mapping.
func TestMatrixSender_Send_EventFormatting(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		eventType  string
		wantPrefix string
	}{
		{"created", eventTypeIncidentCreated, "[DOWN] API health"},
		{"resolved", eventTypeIncidentResolved, "[RECOVERED] API health"},
		{"escalated", eventTypeIncidentEscalated, "[ESCALATED] API health"},
		{"reopened", eventTypeIncidentReopened, "[REOPENED] API health"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sub := require.New(t)

			var gotBody []byte

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				gotBody, _ = io.ReadAll(req.Body)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			payload := matrixTestPayload(tc.eventType, models.JSONMap{
				"homeserverUrl": srv.URL,
				"accessToken":   "syt_secret_token",
				"roomId":        "!room:matrix.org",
			})
			payload.Incident.ResolvedAt = timePtr(payload.Incident.StartedAt.Add(5 * time.Minute))

			sender := &MatrixSender{}
			sub.NoError(sender.Send(context.Background(), newJobCtx(), payload))

			var event matrixMessageEvent
			sub.NoError(json.Unmarshal(gotBody, &event))
			sub.True(strings.HasPrefix(event.Body, tc.wantPrefix), "body=%q", event.Body)
		})
	}
}

// TestMatrixSender_Send_CommentEvent verifies incident.comment renders via
// the shared comment helpers, same as every other card-style sender.
func TestMatrixSender_Send_CommentEvent(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotBody, _ = io.ReadAll(req.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	payload := matrixTestPayload(eventTypeIncidentComment, models.JSONMap{
		"homeserverUrl": srv.URL,
		"accessToken":   "syt_secret_token",
		"roomId":        "!room:matrix.org",
	})
	payload.Comment = &CommentInfo{Text: "investigating now", AuthorName: "Alice"}

	sender := &MatrixSender{}
	r.NoError(sender.Send(context.Background(), newJobCtx(), payload))

	var event matrixMessageEvent
	r.NoError(json.Unmarshal(gotBody, &event))
	r.Contains(event.Body, "Comment on API health")
	r.Contains(event.Body, "Alice: investigating now")
}

// TestMatrixSender_Send_IncidentLinkInHTML verifies the HTML variant carries
// a link to the incident in the dashboard when AppBaseURL/OrgSlug are set.
func TestMatrixSender_Send_IncidentLinkInHTML(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotBody, _ = io.ReadAll(req.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	payload := matrixTestPayload(eventTypeIncidentCreated, models.JSONMap{
		"homeserverUrl": srv.URL,
		"accessToken":   "syt_secret_token",
		"roomId":        "!room:matrix.org",
	})

	sender := &MatrixSender{}
	r.NoError(sender.Send(context.Background(), newJobCtx(), payload))

	var event matrixMessageEvent
	r.NoError(json.Unmarshal(gotBody, &event))
	r.Contains(event.FormattedBody, `<a href="https://solidping.example.com/dash0/orgs/acme/incidents/018e4a2b-incident">`)
}

// TestMatrixSender_Send_MissingSettings covers the three required-field
// errors.
func TestMatrixSender_Send_MissingSettings(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		settings models.JSONMap
		wantErr  error
	}{
		{
			"missing homeserver",
			models.JSONMap{"accessToken": "t", "roomId": "!r:s"},
			ErrMatrixHomeserverNotConfigured,
		},
		{
			"missing access token",
			models.JSONMap{"homeserverUrl": "https://matrix.org", "roomId": "!r:s"},
			ErrMatrixAccessTokenNotConfigured,
		},
		{
			"missing room",
			models.JSONMap{"homeserverUrl": "https://matrix.org", "accessToken": "t"},
			ErrMatrixRoomNotConfigured,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sub := require.New(t)
			payload := matrixTestPayload(eventTypeIncidentCreated, tc.settings)
			sender := &MatrixSender{}
			err := sender.Send(context.Background(), newJobCtx(), payload)
			sub.ErrorIs(err, tc.wantErr)
		})
	}
}

// TestClassifyMatrixResponse covers the error-handling table from the spec:
// 401/403/404 are permanent, 429 (with and without retry_after_ms) and 5xx
// are retryable, everything else falls back to a generic non-retryable error.
func TestClassifyMatrixResponse(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		statusCode  int
		body        string
		wantErr     error
		wantRetry   bool
		wantMessage string
	}{
		{"success 200", http.StatusOK, ``, nil, false, ""},
		{"success 204", http.StatusNoContent, ``, nil, false, ""},
		{
			"unauthorized", http.StatusUnauthorized, `{"errcode":"M_UNKNOWN_TOKEN"}`,
			ErrMatrixUnauthorized, false, "",
		},
		{
			"forbidden", http.StatusForbidden, `{"errcode":"M_FORBIDDEN"}`,
			ErrMatrixForbidden, false, "",
		},
		{
			"not found", http.StatusNotFound, `{"errcode":"M_NOT_FOUND"}`,
			ErrMatrixRoomNotFound, false, "",
		},
		{
			"rate limited no retry_after", http.StatusTooManyRequests, `{"errcode":"M_LIMIT_EXCEEDED"}`,
			ErrMatrixRateLimited, true, "",
		},
		{
			"rate limited with retry_after", http.StatusTooManyRequests,
			`{"errcode":"M_LIMIT_EXCEEDED","retry_after_ms":2000}`,
			ErrMatrixRateLimited, true, "2000ms",
		},
		{
			"server error", http.StatusBadGateway, `{"errcode":"M_UNKNOWN"}`,
			ErrMatrixServerError, true, "",
		},
		{
			"other client error", http.StatusBadRequest, `{"errcode":"M_UNRECOGNIZED"}`,
			errMatrixRequestFailed, false, "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sub := require.New(t)
			err := classifyMatrixResponse(tc.statusCode, []byte(tc.body))

			if tc.wantErr == nil {
				sub.NoError(err)

				return
			}

			sub.Error(err)
			sub.ErrorIs(err, tc.wantErr)
			sub.Equal(tc.wantRetry, jobdef.IsRetryable(err), "retryable mismatch for %s", tc.name)

			if tc.wantMessage != "" {
				sub.Contains(err.Error(), tc.wantMessage)
			}
		})
	}
}

// TestMatrixSender_Send_ErrorResponseSurfacesAsFailure verifies a non-2xx
// homeserver response fails the send with the classified error.
func TestMatrixSender_Send_ErrorResponseSurfacesAsFailure(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN","error":"not in room"}`))
	}))
	defer srv.Close()

	payload := matrixTestPayload(eventTypeIncidentCreated, models.JSONMap{
		"homeserverUrl": srv.URL,
		"accessToken":   "syt_secret_token",
		"roomId":        "!room:matrix.org",
	})

	sender := &MatrixSender{}
	err := sender.Send(context.Background(), newJobCtx(), payload)
	r.ErrorIs(err, ErrMatrixForbidden)
	r.False(jobdef.IsRetryable(err))
}

// TestMatrixSender_Send_RateLimitedIsRetryable verifies a 429 from the
// homeserver during Send propagates as a retryable error.
func TestMatrixSender_Send_RateLimitedIsRetryable(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"errcode":"M_LIMIT_EXCEEDED","retry_after_ms":500}`))
	}))
	defer srv.Close()

	payload := matrixTestPayload(eventTypeIncidentCreated, models.JSONMap{
		"homeserverUrl": srv.URL,
		"accessToken":   "syt_secret_token",
		"roomId":        "!room:matrix.org",
	})

	sender := &MatrixSender{}
	err := sender.Send(context.Background(), newJobCtx(), payload)
	r.ErrorIs(err, ErrMatrixRateLimited)
	r.True(jobdef.IsRetryable(err))
}
