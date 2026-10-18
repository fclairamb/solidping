package notifications

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// fakeA2ACall is one JSON-RPC request the fake agent received.
type fakeA2ACall struct {
	Method  string
	Params  map[string]any
	Headers http.Header
}

// fakeA2AAgent serves an Agent Card at the well-known path and a JSON-RPC
// endpoint at /rpc.
type fakeA2AAgent struct {
	srv *httptest.Server

	mu         sync.Mutex
	calls      []fakeA2ACall
	cardHits   int
	cardHeader http.Header

	// card returns the card JSON for the server's base URL. Nil serves a
	// 1.0 card pointing at /rpc.
	card       func(base string) string
	cardStatus int
	rpcStatus  int
	// result is the JSON-RPC result (or, with rpcError, ignored).
	result   func(call fakeA2ACall) any
	rpcError bool
}

func newFakeA2AAgent(t *testing.T, configure func(agent *fakeA2AAgent)) *fakeA2AAgent {
	t.Helper()

	agent := &fakeA2AAgent{}
	if configure != nil {
		configure(agent)
	}

	agent.srv = httptest.NewUnstartedServer(http.HandlerFunc(agent.handle))
	agent.srv.Start()
	t.Cleanup(agent.srv.Close)

	return agent
}

func a2aCardV1(base string) string {
	return `{"name":"acme investigator","description":"Investigates incidents","version":"1.0.0",
"supportedInterfaces":[
 {"url":"` + base + `/grpc","protocolBinding":"GRPC","protocolVersion":"1.0"},
 {"url":"` + base + `/rpc","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
"capabilities":{},"defaultInputModes":["text/plain"],"defaultOutputModes":["text/plain"],
"skills":[{"id":"investigate","name":"Investigate","description":"Finds the root cause","tags":["ops"]}]}`
}

func a2aCardV03(base string) string {
	return `{"name":"acme legacy","description":"0.3 agent","version":"1.0.0","protocolVersion":"0.3.0",
"url":"` + base + `/rpc","preferredTransport":"JSONRPC",
"capabilities":{},"defaultInputModes":["text/plain"],"defaultOutputModes":["text/plain"],"skills":[]}`
}

func (a *fakeA2AAgent) base() string {
	return a.srv.URL
}

func (a *fakeA2AAgent) handle(w http.ResponseWriter, req *http.Request) {
	switch {
	case req.Method == http.MethodGet && req.URL.Path == a2aWellKnownCardPath:
		a.mu.Lock()
		a.cardHits++
		a.cardHeader = req.Header.Clone()
		a.mu.Unlock()

		if a.cardStatus != 0 {
			w.WriteHeader(a.cardStatus)

			return
		}

		cardFn := a.card
		if cardFn == nil {
			cardFn = a2aCardV1
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, cardFn(a.base()))
	case req.Method == http.MethodPost && req.URL.Path == "/rpc":
		a.handleRPC(w, req)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (a *fakeA2AAgent) handleRPC(w http.ResponseWriter, req *http.Request) {
	var rpc struct {
		ID     any            `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}

	_ = json.NewDecoder(req.Body).Decode(&rpc)

	call := fakeA2ACall{Method: rpc.Method, Params: rpc.Params, Headers: req.Header.Clone()}

	a.mu.Lock()
	a.calls = append(a.calls, call)
	a.mu.Unlock()

	if a.rpcStatus != 0 {
		w.WriteHeader(a.rpcStatus)

		return
	}

	resp := map[string]any{"jsonrpc": "2.0", "id": rpc.ID}

	switch {
	case a.rpcError:
		resp["error"] = map[string]any{"code": -32009, "message": "version not supported"}
	case a.result != nil:
		resp["result"] = a.result(call)
	default:
		resp["result"] = map[string]any{"message": map[string]any{
			"messageId": "agent-reply-1", "role": "ROLE_AGENT",
			"parts": []any{map[string]any{"text": "Looking into it"}},
		}}
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (a *fakeA2AAgent) snapshot() ([]fakeA2ACall, int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]fakeA2ACall(nil), a.calls...), a.cardHits
}

// fakeA2AComments records AddCommentFromA2A calls.
type fakeA2AComments struct {
	mu    sync.Mutex
	texts []string
	authr []string
}

func (f *fakeA2AComments) AddCommentFromA2A(
	_ context.Context, _, _, _, integrationName, text string,
) (*models.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.texts = append(f.texts, text)
	f.authr = append(f.authr, integrationName)

	return &models.Event{}, nil
}

func (f *fakeA2AComments) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.texts...)
}

// fakePollJobs records CreateJob calls; every other method of the interface
// is unused and panics through the nil embedded value.
type fakePollJobs struct {
	jobsvc.Service

	mu   sync.Mutex
	jobs []fakeCreatedJob
}

type fakeCreatedJob struct {
	OrgUID      string
	Type        string
	Config      json.RawMessage
	ScheduledAt time.Time
}

func (f *fakePollJobs) CreateJob(
	_ context.Context, orgUID, jobType string, config json.RawMessage, opts *jobsvc.JobOptions,
) (*models.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	job := fakeCreatedJob{OrgUID: orgUID, Type: jobType, Config: config}
	if opts != nil && opts.ScheduledAt != nil {
		job.ScheduledAt = *opts.ScheduledAt
	}

	f.jobs = append(f.jobs, job)

	return &models.Job{UID: uuid.NewString()}, nil
}

func (f *fakePollJobs) all() []fakeCreatedJob {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]fakeCreatedJob(nil), f.jobs...)
}

type a2aHarness struct {
	jctx     *jobdef.JobContext
	comments *fakeA2AComments
	jobs     *fakePollJobs
}

func newA2AHarness(guard *egress.Guard) *a2aHarness {
	h := &a2aHarness{comments: &fakeA2AComments{}, jobs: &fakePollJobs{}}
	h.jctx = &jobdef.JobContext{
		Logger:   slog.Default(),
		Services: &services.Registry{Jobs: h.jobs, A2AComments: h.comments, EgressGuard: guard},
	}

	return h
}

// a2aTestPayload is an A2A payload with a fresh integration uid (the card
// cache is keyed on it) and a fresh incident uid.
func a2aTestPayload(settings models.JSONMap, eventType string) *Payload {
	payload := testPayload(settings)
	payload.EventType = eventType
	payload.EventKey = "job-" + uuid.NewString()
	payload.OrgSlug = "acme"
	payload.AppBaseURL = "https://solidping.acme.com"
	payload.Incident.UID = uuid.NewString()
	payload.Integration.UID = uuid.NewString()
	payload.Integration.Name = "investigator"
	payload.Integration.Type = models.ConnectionTypeA2A

	slug := "api-health"
	payload.Check.Slug = &slug

	return payload
}

func requirePermanentA2A(r *require.Assertions, err error) {
	r.Error(err)
	r.False(jobdef.IsRetryable(err), "must not be retryable: %v", err)
	r.False(IsNetworkError(err), "the notification job would retry a network error: %v", err)
}

func requireRetryableA2A(r *require.Assertions, err error) {
	r.Error(err)
	r.True(jobdef.IsRetryable(err), "must be retryable: %v", err)
}

func TestA2ASender_OpenedIncidentSendsMessage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	agent := newFakeA2AAgent(t, nil)
	h := newA2AHarness(nil)
	payload := a2aTestPayload(models.JSONMap{
		A2ASettingAgentCardURL: agent.base(),
		"instructions":         "Investigate and report your findings, do not restart anything",
	}, eventTypeIncidentCreated)

	r.NoError((&A2ASender{}).Send(t.Context(), h.jctx, payload))

	calls, cardHits := agent.snapshot()
	r.Equal(1, cardHits)
	r.Len(calls, 1)

	call := calls[0]
	r.Equal("SendMessage", call.Method)
	r.Equal("1.0", call.Headers.Get(a2a.SvcParamVersion))

	config, _ := call.Params["configuration"].(map[string]any)
	r.Equal(true, config["returnImmediately"])

	msg, _ := call.Params["message"].(map[string]any)
	r.Equal(payload.Incident.UID, msg["contextId"])
	r.NotContains(msg, "taskId")
	r.Equal(payload.MessageID, msg["messageId"])

	parts, _ := msg["parts"].([]any)
	r.Len(parts, 2)

	text, _ := parts[0].(map[string]any)["text"].(string)
	r.Contains(text, "SolidPing incident opened: API health")
	r.Contains(text, "do not restart anything")
	r.Contains(text, "MCP")
	r.Contains(text, "https://solidping.acme.com/d/orgs/acme/incidents/"+payload.Incident.UID)

	data, _ := parts[1].(map[string]any)["data"].(map[string]any)
	r.Equal(payload.Incident.UID, data["incidentUid"])
	r.Equal(payload.Check.UID, data["checkUid"])
	r.Equal("api-health", data["checkSlug"])
	r.Equal("acme", data["orgSlug"])
	r.Equal(eventTypeIncidentCreated, data["eventType"])
	r.Equal("active", data["status"])
	r.Equal("https://solidping.acme.com/api/mcp", data["mcpUrl"])
	for _, key := range []string{"failureReason", "startedAt", "resolvedAt", "dashboardUrl"} {
		r.Contains(data, key)
	}

	// A Message response is recorded as one comment.
	r.Equal([]string{"Looking into it"}, h.comments.all())
	r.Empty(h.jobs.all())
}

func TestA2ASender_LifecycleEventsShareTheContext(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	agent := newFakeA2AAgent(t, nil)
	h := newA2AHarness(nil)
	base := a2aTestPayload(models.JSONMap{A2ASettingAgentCardURL: agent.base()}, eventTypeIncidentCreated)

	events := []string{
		eventTypeIncidentCreated, eventTypeIncidentResolved,
		eventTypeIncidentAcknowledged, eventTypeIncidentUnacknowledged,
	}
	for _, eventType := range events {
		payload := *base
		payload.EventType = eventType
		payload.EventKey = "job-" + eventType
		r.NoError((&A2ASender{}).Send(t.Context(), h.jctx, &payload))
	}

	calls, cardHits := agent.snapshot()
	r.Len(calls, len(events))
	r.Equal(1, cardHits, "the card is cached per integration")

	ids := map[string]bool{}
	for _, call := range calls {
		msg, _ := call.Params["message"].(map[string]any)
		r.Equal(base.Incident.UID, msg["contextId"])
		r.NotContains(msg, "taskId")
		id, _ := msg["messageId"].(string)
		ids[id] = true
	}

	r.Len(ids, len(events), "each event gets its own messageId")
}

func TestA2ASender_MessageIDIsStableAcrossRetries(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	agent := newFakeA2AAgent(t, nil)
	h := newA2AHarness(nil)
	payload := a2aTestPayload(models.JSONMap{A2ASettingAgentCardURL: agent.base()}, eventTypeIncidentCreated)

	retry := *payload
	other := *payload
	other.EventKey = "job-other-event"

	for _, p := range []*Payload{payload, &retry, &other} {
		r.NoError((&A2ASender{}).Send(t.Context(), h.jctx, p))
	}

	calls, _ := agent.snapshot()
	r.Len(calls, 3)

	messageID := func(i int) string {
		msg, _ := calls[i].Params["message"].(map[string]any)

		id, _ := msg["messageId"].(string)

		return id
	}

	r.Equal(messageID(0), messageID(1), "a retried job resends the same messageId")
	r.NotEqual(messageID(0), messageID(2), "another event of the incident has another messageId")
}

func TestA2ASender_SkippedEventsSendNothing(t *testing.T) {
	t.Parallel()

	for _, eventType := range []string{
		"incident.degraded", "incident.slo_burn", "check.deleted",
		eventTypeIncidentComment, eventTypeIncidentReminder, eventTypeIncidentEscalated,
	} {
		t.Run(eventType, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			agent := newFakeA2AAgent(t, nil)
			h := newA2AHarness(nil)
			payload := a2aTestPayload(models.JSONMap{A2ASettingAgentCardURL: agent.base()}, eventType)

			r.NoError((&A2ASender{}).Send(t.Context(), h.jctx, payload))

			calls, cardHits := agent.snapshot()
			r.Empty(calls)
			r.Zero(cardHits)
			r.Empty(h.comments.all())
		})
	}
}

func TestA2ASender_AuthHeaders(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		settings models.JSONMap
		header   string
		value    string
	}{
		{"bearer", models.JSONMap{"auth": A2AAuthBearer, "token": "s3cret"}, "Authorization", "Bearer s3cret"},
		{"api_key", models.JSONMap{
			"auth": A2AAuthAPIKey, "api_key_header": "X-Api-Key", "api_key_value": "k3y",
		}, "X-Api-Key", "k3y"},
		{"none", models.JSONMap{"auth": A2AAuthNone}, "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			agent := newFakeA2AAgent(t, nil)
			h := newA2AHarness(nil)
			tc.settings[A2ASettingAgentCardURL] = agent.base()
			payload := a2aTestPayload(tc.settings, eventTypeIncidentCreated)

			r.NoError((&A2ASender{}).Send(t.Context(), h.jctx, payload))

			calls, _ := agent.snapshot()
			r.Len(calls, 1)

			if tc.header == "" {
				r.Empty(calls[0].Headers.Get("Authorization"))
				r.Empty(agent.cardHeader.Get("Authorization"))

				return
			}

			r.Equal(tc.value, calls[0].Headers.Get(tc.header))
			r.Equal(tc.value, agent.cardHeader.Get(tc.header))
		})
	}
}

func TestA2ASender_BadCardsArePermanent(t *testing.T) {
	t.Parallel()

	cases := map[string]func(agent *fakeA2AAgent){
		"card 404":       func(agent *fakeA2AAgent) { agent.cardStatus = http.StatusNotFound },
		"malformed card": func(agent *fakeA2AAgent) { agent.card = func(string) string { return "{not json" } },
		"no JSONRPC interface": func(agent *fakeA2AAgent) {
			agent.card = func(base string) string {
				return `{"name":"grpc only","supportedInterfaces":[{"url":"` + base +
					`/grpc","protocolBinding":"GRPC","protocolVersion":"1.0"}],"capabilities":{}}`
			}
		},
	}

	for name, configure := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			agent := newFakeA2AAgent(t, configure)
			h := newA2AHarness(nil)
			payload := a2aTestPayload(models.JSONMap{A2ASettingAgentCardURL: agent.base()}, eventTypeIncidentCreated)

			err := (&A2ASender{}).Send(t.Context(), h.jctx, payload)
			requirePermanentA2A(r, err)

			calls, _ := agent.snapshot()
			r.Empty(calls)
		})
	}
}

func TestA2ASender_V03CardUsesTheCompatCodec(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	agent := newFakeA2AAgent(t, func(agent *fakeA2AAgent) {
		agent.card = a2aCardV03
		agent.result = func(fakeA2ACall) any {
			return map[string]any{
				"kind": "task", "id": "task-legacy", "contextId": "ctx",
				"status": map[string]any{"state": "completed"},
				"artifacts": []any{map[string]any{
					"artifactId": "a1",
					"parts":      []any{map[string]any{"kind": "text", "text": "DNS is down at acme.com"}},
				}},
			}
		}
	})
	h := newA2AHarness(nil)
	payload := a2aTestPayload(models.JSONMap{A2ASettingAgentCardURL: agent.base()}, eventTypeIncidentCreated)

	r.NoError((&A2ASender{}).Send(t.Context(), h.jctx, payload))

	calls, _ := agent.snapshot()
	r.Len(calls, 1)
	r.Equal("message/send", calls[0].Method)
	r.Empty(calls[0].Headers.Get(a2a.SvcParamVersion), "0.3 predates the version header")

	msg, _ := calls[0].Params["message"].(map[string]any)
	r.Equal(payload.Incident.UID, msg["contextId"])

	r.Equal([]string{"DNS is down at acme.com"}, h.comments.all())

	info, err := ResolveA2AAgent(t.Context(), nil, models.JSONMap{A2ASettingAgentCardURL: agent.base()})
	r.NoError(err)
	r.Equal("0.3.0", info.ProtocolVersion)
}

func TestA2ASender_ErrorClassification(t *testing.T) {
	t.Parallel()

	t.Run("503 is retryable", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		agent := newFakeA2AAgent(t, func(agent *fakeA2AAgent) { agent.rpcStatus = http.StatusServiceUnavailable })
		h := newA2AHarness(nil)
		payload := a2aTestPayload(models.JSONMap{A2ASettingAgentCardURL: agent.base()}, eventTypeIncidentCreated)

		requireRetryableA2A(r, (&A2ASender{}).Send(t.Context(), h.jctx, payload))
	})

	t.Run("connection refused is retryable", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		r.NoError(err)
		closed := "http://" + listener.Addr().String()
		r.NoError(listener.Close())

		agent := newFakeA2AAgent(t, func(agent *fakeA2AAgent) {
			agent.card = func(string) string { return a2aCardV1(closed) }
		})
		h := newA2AHarness(nil)
		payload := a2aTestPayload(models.JSONMap{A2ASettingAgentCardURL: agent.base()}, eventTypeIncidentCreated)

		requireRetryableA2A(r, (&A2ASender{}).Send(t.Context(), h.jctx, payload))
	})

	t.Run("401 is permanent", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		agent := newFakeA2AAgent(t, func(agent *fakeA2AAgent) { agent.rpcStatus = http.StatusUnauthorized })
		h := newA2AHarness(nil)
		payload := a2aTestPayload(models.JSONMap{A2ASettingAgentCardURL: agent.base()}, eventTypeIncidentCreated)

		err := (&A2ASender{}).Send(t.Context(), h.jctx, payload)
		requirePermanentA2A(r, err)
		r.ErrorIs(err, ErrA2ARequestFailed)
	})

	t.Run("JSON-RPC error is permanent", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		agent := newFakeA2AAgent(t, func(agent *fakeA2AAgent) { agent.rpcError = true })
		h := newA2AHarness(nil)
		payload := a2aTestPayload(models.JSONMap{A2ASettingAgentCardURL: agent.base()}, eventTypeIncidentCreated)

		err := (&A2ASender{}).Send(t.Context(), h.jctx, payload)
		requirePermanentA2A(r, err)
		r.ErrorIs(err, ErrA2ARequestFailed)
	})
}

func TestA2ASender_EgressGuard(t *testing.T) {
	t.Parallel()

	t.Run("loopback card URL is rejected", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		agent := newFakeA2AAgent(t, nil)
		h := newA2AHarness(egress.New(false))
		payload := a2aTestPayload(models.JSONMap{A2ASettingAgentCardURL: agent.base()}, eventTypeIncidentCreated)

		err := (&A2ASender{}).Send(t.Context(), h.jctx, payload)
		requirePermanentA2A(r, err)
		r.ErrorIs(err, ErrSenderURLInvalid)

		calls, cardHits := agent.snapshot()
		r.Empty(calls)
		r.Zero(cardHits)
	})

	t.Run("private interface URL in the card is rejected", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "[::1]:0")
		if err != nil {
			t.Skip("no IPv6 loopback: ", err)
		}

		// The card is served from ::1, which this guard treats as public; the
		// interface it advertises is on 127.0.0.1, judged as production does.
		internal := newFakeA2AAgent(t, nil)
		card := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, a2aCardV1(internal.base()))
		}))
		card.Listener = listener
		card.Start()
		t.Cleanup(card.Close)

		guard := egress.New(false, egress.WithPublicOverride(net.ParseIP("::1")))
		h := newA2AHarness(guard)
		payload := a2aTestPayload(models.JSONMap{A2ASettingAgentCardURL: card.URL}, eventTypeIncidentCreated)

		err = (&A2ASender{}).Send(t.Context(), h.jctx, payload)
		requirePermanentA2A(r, err)
		r.ErrorIs(err, ErrSenderURLInvalid)

		calls, _ := internal.snapshot()
		r.Empty(calls, "the private interface must never be called")
	})
}

func TestA2ASender_WorkingTaskEnqueuesAPoll(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	agent := newFakeA2AAgent(t, func(agent *fakeA2AAgent) {
		agent.result = func(fakeA2ACall) any {
			return map[string]any{"task": map[string]any{
				"id": "task-1", "contextId": "ctx", "status": map[string]any{"state": "TASK_STATE_WORKING"},
			}}
		}
	})
	h := newA2AHarness(nil)
	payload := a2aTestPayload(models.JSONMap{A2ASettingAgentCardURL: agent.base()}, eventTypeIncidentCreated)

	before := time.Now()
	r.NoError((&A2ASender{}).Send(t.Context(), h.jctx, payload))

	r.Empty(h.comments.all(), "a running task records nothing yet")

	jobs := h.jobs.all()
	r.Len(jobs, 1)
	r.Equal(string(jobdef.JobTypeA2ATaskPoll), jobs[0].Type)
	r.Equal(payload.Integration.OrganizationUID, jobs[0].OrgUID)
	r.WithinDuration(before.Add(A2APollFirstDelay), jobs[0].ScheduledAt, 2*time.Second)

	var cfg A2ATaskPollConfig
	r.NoError(json.Unmarshal(jobs[0].Config, &cfg))
	r.Equal(payload.Integration.UID, cfg.IntegrationUID)
	r.Equal(payload.Incident.UID, cfg.A2AIncidentUID)
	r.Equal("task-1", cfg.TaskID)
	r.Zero(cfg.Attempt)
	r.WithinDuration(before.Add(A2APollMaxWait), cfg.Deadline, 2*time.Second)
	r.NotContains(string(jobs[0].Config), `"incidentUid"`,
		"the incident cancel sweep must not drop an investigation's poll")
}

func a2aTask(state a2a.TaskState, status string) *a2a.Task {
	task := &a2a.Task{ID: "t", ContextID: "c", Status: a2a.TaskStatus{State: state}}
	if status != "" {
		task.Status.Message = a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(status))
	}

	return task
}

func TestA2ATaskComment(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	failed := A2ATaskComment(a2aTask(a2a.TaskStateFailed, "boom"))
	r.Contains(failed, "task failed")
	r.Contains(failed, "boom")
	r.Contains(A2ATaskComment(a2aTask(a2a.TaskStateRejected, "")), "task rejected")

	input := A2ATaskComment(a2aTask(a2a.TaskStateInputRequired, "Restart the db?"))
	r.Contains(input, "waiting for a human")
	r.Contains(input, "Restart the db?")

	r.True(strings.HasPrefix(A2ATaskComment(a2aTask(a2a.TaskStateCompleted, "")), a2aCommentPrefix))

	done := a2aTask(a2a.TaskStateCompleted, "")
	done.Artifacts = []*a2a.Artifact{{ID: "a", Parts: a2a.ContentParts{
		a2a.NewTextPart("root cause: expired cert"), a2a.NewDataPart(map[string]any{"ignored": true}),
	}}}
	r.Equal("root cause: expired cert", A2ATaskComment(done))
}
