package jmap_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jmap"
)

// fakeJMAPServer implements just enough JMAP to drive the manager: session
// discovery, Mailbox/get with two mailboxes (Inbox, Processed), Email/query,
// Email/get, and Email/set move.
type fakeJMAPServer struct {
	srv     *httptest.Server
	t       *testing.T
	mu      sync.Mutex
	emails  map[string]map[string]bool // emailID -> mailboxIds
	moves   atomic.Int32
	queried atomic.Int32
}

func newFakeJMAPServer(t *testing.T) *fakeJMAPServer {
	t.Helper()

	f := &fakeJMAPServer{
		t: t,
		emails: map[string]map[string]bool{
			"e1": {"inbox": true},
			"e2": {"inbox": true},
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jmap", f.handleSession)
	mux.HandleFunc("/jmap", f.handleJMAP)
	f.srv = httptest.NewServer(mux)

	return f
}

func (f *fakeJMAPServer) Close() { f.srv.Close() }

func (f *fakeJMAPServer) handleSession(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	payload := `{"capabilities":{` +
		`"urn:ietf:params:jmap:core":{},` +
		`"urn:ietf:params:jmap:mail":{}},` +
		`"accounts":{"acc-1":{"name":"x","isPersonal":true}},` +
		`"primaryAccounts":{"urn:ietf:params:jmap:mail":"acc-1"},` +
		`"apiUrl":"` + f.srv.URL + `/jmap",` +
		`"state":"s"}`
	_, _ = w.Write([]byte(payload))
}

func (f *fakeJMAPServer) handleJMAP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	bodyStr := string(body)

	w.Header().Set("Content-Type", "application/json")

	switch {
	case strings.Contains(bodyStr, `"Mailbox/get"`):
		_, _ = w.Write([]byte(`{"methodResponses":[["Mailbox/get",{"list":[
			{"id":"inbox","name":"Inbox","role":"inbox"},
			{"id":"processed","name":"Processed"}
		]},"c0"]],"sessionState":"s"}`))
	case strings.Contains(bodyStr, `"Email/query"`):
		f.queried.Add(1)

		mailbox := extractInMailbox(bodyStr)

		f.mu.Lock()

		ids := []string{}
		for id, mboxes := range f.emails {
			if mboxes[mailbox] {
				ids = append(ids, id)
			}
		}

		f.mu.Unlock()

		buf, mErr := json.Marshal(ids)
		if mErr != nil {
			f.t.Fatalf("marshal ids: %v", mErr)
		}

		queryResp := `{"methodResponses":[["Email/query",` +
			`{"ids":` + string(buf) + `},"c0"]],` +
			`"sessionState":"s"}`
		_, _ = w.Write([]byte(queryResp))
	case strings.Contains(bodyStr, `"Email/get"`):
		_, _ = w.Write([]byte(`{"methodResponses":[["Email/get",{"list":[
			{"id":"e1","receivedAt":"2026-01-01T00:00:00Z","from":[{"email":"a@x"}],"to":[{"email":"x@y"}]},
			{"id":"e2","receivedAt":"2026-01-01T00:01:00Z","from":[{"email":"b@x"}],"to":[{"email":"y@y"}]}
		]},"c0"]],"sessionState":"s"}`))
	case strings.Contains(bodyStr, `"Email/set"`):
		f.moves.Add(1)
		f.mu.Lock()
		// any update sets the email's mailboxIds to processed only
		for id := range f.emails {
			if strings.Contains(bodyStr, `"`+id+`"`) {
				f.emails[id] = map[string]bool{"processed": true}
			}
		}
		f.mu.Unlock()
		setResp := `{"methodResponses":[["Email/set",` +
			`{"updated":{"e1":null,"e2":null}},"c0"]],` +
			`"sessionState":"s"}`
		_, _ = w.Write([]byte(setResp))
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func extractInMailbox(body string) string {
	const marker = `"inMailbox":"`

	idx := strings.Index(body, marker)
	if idx < 0 {
		return ""
	}

	rest := body[idx+len(marker):]

	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}

	return rest[:end]
}

// stubHandler is a Handler test double.
type stubHandler struct {
	outcome jmap.Outcome
	called  atomic.Int32
	err     error
}

func (s *stubHandler) HandleEmail(_ context.Context, _ *jmap.Mailboxes, _ jmap.Email) (jmap.Outcome, error) {
	s.called.Add(1)

	return s.outcome, s.err
}

func TestManagerProcessesEmailsViaSecondHandler(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := newFakeJMAPServer(t)
	defer srv.Close()

	cfg := jmap.Config{
		Enabled:       true,
		SessionURL:    srv.srv.URL + "/.well-known/jmap",
		Username:      "u",
		Password:      "p",
		AddressDomain: "ingest.example",
	}
	cfg.ApplyDefaults()

	first := &stubHandler{outcome: jmap.OutcomeIgnored}
	second := &stubHandler{outcome: jmap.OutcomeProcessed}

	client := jmap.NewClient(&cfg)
	_, err := client.DiscoverSession(context.Background())
	r.NoError(err)

	mboxes := &jmap.Mailboxes{
		Inbox:     &jmap.Mailbox{ID: "inbox"},
		Processed: &jmap.Mailbox{ID: "processed"},
	}

	mgr := jmap.NewManager(nil)
	mgr.RegisterHandler(first)
	mgr.RegisterHandler(second)

	r.NoError(mgr.SyncEmailsForTest(context.Background(), client, mboxes, &cfg))

	r.GreaterOrEqual(first.called.Load(), int32(2), "first handler called for both emails")
	r.GreaterOrEqual(second.called.Load(), int32(2), "second handler called after first ignored")
	r.GreaterOrEqual(srv.moves.Load(), int32(1), "Email/set move issued")
}

func TestManagerStatusReportsLastError(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	mgr := jmap.NewManager(nil)
	mgr.RecordErrorForTest(errSentinel)

	status := mgr.GetStatus()
	r.Equal("test sentinel", status.LastError)
	r.False(status.Connected)
}

var errSentinel = errors.New("test sentinel")

func TestTestConnectionDiscoversAndResolvesMailboxes(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := newFakeJMAPServer(t)
	defer srv.Close()

	mgr := jmap.NewManager(nil)
	cfg := &jmap.Config{
		Enabled:       true,
		SessionURL:    srv.srv.URL + "/.well-known/jmap",
		Username:      "u",
		Password:      "p",
		AddressDomain: "ingest.example",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mboxes, err := mgr.TestConnection(ctx, cfg)
	r.NoError(err)
	r.NotNil(mboxes)
	r.Equal("inbox", mboxes.Inbox.ID)
	r.Equal("processed", mboxes.Processed.ID)
}

func TestTriggerSyncIsNonBlocking(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	mgr := jmap.NewManager(nil)
	r.NoError(mgr.TriggerSync(context.Background()))
	r.NoError(mgr.TriggerSync(context.Background()))
	r.NoError(mgr.TriggerSync(context.Background()))
}

// JSONMapToConfig must unwrap the {"value": ...} envelope produced by
// SetSystemParameter; reading the wrapper directly used to leak through and
// yield a zero Config (sessionUrl/username/etc. all empty).
func TestJSONMapToConfigUnwrapsParameterEnvelope(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	wrapped := models.JSONMap{
		"value": map[string]any{
			"enabled":       true,
			"sessionUrl":    "https://mail.example.com/.well-known/jmap",
			"username":      "admin@example.com",
			"password":      "secret",
			"addressDomain": "example.com",
		},
	}

	cfg, err := jmap.JSONMapToConfig(wrapped)
	r.NoError(err)
	r.NotNil(cfg)
	r.True(cfg.Enabled)
	r.Equal("https://mail.example.com/.well-known/jmap", cfg.SessionURL)
	r.Equal("admin@example.com", cfg.Username)
	r.Equal("secret", cfg.Password)
	r.Equal("example.com", cfg.AddressDomain)

	// Unwrapped (legacy / direct) input should still parse for callers that
	// already extracted the inner value.
	bare := models.JSONMap{
		"sessionUrl": "https://bare.example.com/.well-known/jmap",
		"username":   "bare@example.com",
	}

	cfg, err = jmap.JSONMapToConfig(bare)
	r.NoError(err)
	r.Equal("https://bare.example.com/.well-known/jmap", cfg.SessionURL)
	r.Equal("bare@example.com", cfg.Username)
}
