package jobtypes

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifications"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// pollAgent is a fake A2A agent answering GetTask with one scripted answer
// per call (the last one repeats).
type pollAgent struct {
	srv *httptest.Server

	mu      sync.Mutex
	answers []func(w http.ResponseWriter, id any)
	rpcHits int
}

func taskAnswer(task map[string]any) func(w http.ResponseWriter, id any) {
	return func(w http.ResponseWriter, id any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": task})
	}
}

func statusAnswer(code int) func(w http.ResponseWriter, id any) {
	return func(w http.ResponseWriter, _ any) { w.WriteHeader(code) }
}

func pollTask(state string, extra map[string]any) map[string]any {
	task := map[string]any{"id": "task-1", "contextId": "ctx", "status": map[string]any{"state": state}}
	for k, v := range extra {
		task[k] = v
	}

	return task
}

func newPollAgent(t *testing.T, answers ...func(w http.ResponseWriter, id any)) *pollAgent {
	t.Helper()

	agent := &pollAgent{answers: answers}
	agent.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"name":"acme","supportedInterfaces":[{"url":"`+agent.srv.URL+
				`/rpc","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{}}`)

			return
		}

		var rpc struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(req.Body).Decode(&rpc)

		agent.mu.Lock()
		idx := min(agent.rpcHits, len(agent.answers)-1)
		agent.rpcHits++
		answer := agent.answers[idx]
		agent.mu.Unlock()

		if rpc.Method != "GetTask" {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		answer(w, rpc.ID)
	}))
	t.Cleanup(agent.srv.Close)

	return agent
}

func (a *pollAgent) hits() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.rpcHits
}

type pollComments struct {
	mu    sync.Mutex
	texts []string
}

func (c *pollComments) AddCommentFromA2A(
	_ context.Context, _, _, _, _, text string,
) (*models.Event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.texts = append(c.texts, text)

	return &models.Event{}, nil
}

func (c *pollComments) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]string(nil), c.texts...)
}

type pollEnv struct {
	db          *sqlite.Service
	org         *models.Organization
	incident    *models.Incident
	integration *models.Integration
	comments    *pollComments
	jctx        *jobdef.JobContext
}

func newPollEnv(t *testing.T, agentURL string) *pollEnv {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("a2a-org", "A2A Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	incident := models.NewIncident(org.UID, check.UID, time.Now().Add(-time.Minute), "api is down")
	r.NoError(dbSvc.CreateIncident(ctx, incident))

	integration := models.NewIntegration(org.UID, models.ConnectionTypeA2A, "investigator")
	integration.Settings = models.JSONMap{notifications.A2ASettingAgentCardURL: agentURL}
	r.NoError(dbSvc.CreateChannel(ctx, integration))

	comments := &pollComments{}

	return &pollEnv{
		db: dbSvc, org: org, incident: incident, integration: integration, comments: comments,
		jctx: &jobdef.JobContext{
			DBService: dbSvc,
			Logger:    slog.Default(),
			Services: &services.Registry{
				Jobs:        jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil),
				A2AComments: comments,
			},
		},
	}
}

func (e *pollEnv) config(attempt int, deadline time.Time) notifications.A2ATaskPollConfig {
	return notifications.A2ATaskPollConfig{
		OrgUID: e.org.UID, IntegrationUID: e.integration.UID, A2AIncidentUID: e.incident.UID,
		TaskID: "task-1", Attempt: attempt, Deadline: deadline,
	}
}

func (e *pollEnv) run(t *testing.T, cfg notifications.A2ATaskPollConfig) {
	t.Helper()

	raw, err := json.Marshal(cfg)
	require.NoError(t, err)

	runner, err := (&A2ATaskPollJobDefinition{}).CreateJobRun(raw)
	require.NoError(t, err)
	require.NoError(t, runner.Run(t.Context(), e.jctx))
}

// pollJobs returns the pending a2a_task_poll jobs with their configs.
func (e *pollEnv) pollJobs(t *testing.T) ([]*models.Job, []notifications.A2ATaskPollConfig) {
	t.Helper()

	var jobs []*models.Job
	require.NoError(t, e.db.DB().NewSelect().Model(&jobs).
		Where("type = ?", string(jobdef.JobTypeA2ATaskPoll)).
		Where("status = ?", string(models.JobStatusPending)).
		Where("deleted_at IS NULL").
		Order("scheduled_at ASC").
		Scan(t.Context()))

	configs := make([]notifications.A2ATaskPollConfig, 0, len(jobs))

	for _, job := range jobs {
		raw, err := json.Marshal(job.Config)
		require.NoError(t, err)

		var cfg notifications.A2ATaskPollConfig
		require.NoError(t, json.Unmarshal(raw, &cfg))

		configs = append(configs, cfg)
	}

	return jobs, configs
}

func TestA2ATaskPoll_WorkingThenCompleted(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	agent := newPollAgent(t,
		taskAnswer(pollTask("TASK_STATE_WORKING", nil)),
		taskAnswer(pollTask("TASK_STATE_COMPLETED", map[string]any{"artifacts": []any{map[string]any{
			"artifactId": "a1", "parts": []any{map[string]any{"text": "Root cause: expired TLS cert on acme.com"}},
		}}})),
	)
	env := newPollEnv(t, agent.srv.URL)
	deadline := time.Now().Add(notifications.A2APollMaxWait)

	before := time.Now()
	env.run(t, env.config(0, deadline))

	r.Empty(env.comments.all())

	jobs, configs := env.pollJobs(t)
	r.Len(jobs, 1)
	r.Equal(&env.org.UID, jobs[0].OrganizationUID)
	r.Equal(1, configs[0].Attempt)
	r.WithinDuration(before.Add(notifications.A2APollDelay(1)), jobs[0].ScheduledAt, 2*time.Second)

	env.run(t, configs[0])

	r.Equal([]string{"Root cause: expired TLS cert on acme.com"}, env.comments.all())

	jobs, _ = env.pollJobs(t)
	r.Len(jobs, 1, "a completed task schedules no further poll")
	r.Equal(2, agent.hits())
}

func TestA2ATaskPoll_SettledStatesRecordOneComment(t *testing.T) {
	t.Parallel()

	statusMessage := map[string]any{"status": map[string]any{
		"message": map[string]any{
			"messageId": "m", "role": "ROLE_AGENT",
			"parts": []any{map[string]any{"text": "Should I restart the database?"}},
		},
	}}

	cases := []struct {
		state string
		want  string
	}{
		{"TASK_STATE_FAILED", "task failed"},
		{"TASK_STATE_REJECTED", "task rejected"},
		{"TASK_STATE_INPUT_REQUIRED", "waiting for a human"},
	}

	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			task := pollTask(tc.state, nil)
			if tc.state == "TASK_STATE_INPUT_REQUIRED" {
				status, _ := statusMessage["status"].(map[string]any)
				task["status"] = map[string]any{"state": tc.state, "message": status["message"]}
			}

			agent := newPollAgent(t, taskAnswer(task))
			env := newPollEnv(t, agent.srv.URL)

			env.run(t, env.config(2, time.Now().Add(time.Minute)))

			comments := env.comments.all()
			r.Len(comments, 1)
			r.Contains(comments[0], tc.want)

			if tc.state == "TASK_STATE_INPUT_REQUIRED" {
				r.Contains(comments[0], "Should I restart the database?")
			}

			jobs, _ := env.pollJobs(t)
			r.Empty(jobs, "polling stops")
		})
	}
}

func TestA2ATaskPoll_DeadlineStopsWithANote(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	agent := newPollAgent(t, taskAnswer(pollTask("TASK_STATE_WORKING", nil)))
	env := newPollEnv(t, agent.srv.URL)

	env.run(t, env.config(30, time.Now().Add(-time.Second)))

	r.Equal([]string{notifications.A2ATimeoutNote}, env.comments.all())

	jobs, _ := env.pollJobs(t)
	r.Empty(jobs)
}

func TestA2ATaskPoll_ServerErrorReschedules(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	agent := newPollAgent(t, statusAnswer(http.StatusServiceUnavailable))
	env := newPollEnv(t, agent.srv.URL)

	before := time.Now()
	env.run(t, env.config(2, time.Now().Add(time.Hour)))

	r.Empty(env.comments.all())

	jobs, configs := env.pollJobs(t)
	r.Len(jobs, 1)
	r.Equal(3, configs[0].Attempt)
	r.WithinDuration(before.Add(60*time.Second), jobs[0].ScheduledAt, 2*time.Second)
}

func TestA2ATaskPoll_NotFoundStops(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	agent := newPollAgent(t, statusAnswer(http.StatusNotFound))
	env := newPollEnv(t, agent.srv.URL)

	env.run(t, env.config(0, time.Now().Add(time.Hour)))

	r.Equal(1, agent.hits(), "positive control: the agent was asked")
	r.Empty(env.comments.all())

	jobs, _ := env.pollJobs(t)
	r.Empty(jobs)
}

func TestA2ATaskPoll_DeletedIntegrationStopsSilently(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	agent := newPollAgent(t, taskAnswer(pollTask("TASK_STATE_COMPLETED", nil)))
	env := newPollEnv(t, agent.srv.URL)
	r.NoError(env.db.DeleteChannel(t.Context(), env.integration.UID))

	env.run(t, env.config(0, time.Now().Add(time.Hour)))

	r.Zero(agent.hits())
	r.Empty(env.comments.all())

	jobs, _ := env.pollJobs(t)
	r.Empty(jobs)
}

func TestA2APollDelay_Backoff(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(10*time.Second, notifications.A2APollDelay(0))
	r.Equal(20*time.Second, notifications.A2APollDelay(1))
	r.Equal(40*time.Second, notifications.A2APollDelay(2))
	r.Equal(60*time.Second, notifications.A2APollDelay(3))
	r.Equal(60*time.Second, notifications.A2APollDelay(25))
}

// TestRootJobUID_StableAcrossRetries pins the id the A2A messageId is derived
// from: every attempt of a retried notification job resolves to the first
// job of the chain.
func TestRootJobUID_StableAcrossRetries(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newPollEnv(t, "http://unused.example.com")
	ctx := t.Context()

	first := models.NewJob(&env.org.UID, string(jobdef.JobTypeNotification))
	_, err := env.db.DB().NewInsert().Model(first).Exec(ctx)
	r.NoError(err)

	second := models.NewJob(&env.org.UID, string(jobdef.JobTypeNotification))
	second.PreviousJobUID = &first.UID
	_, err = env.db.DB().NewInsert().Model(second).Exec(ctx)
	r.NoError(err)

	third := models.NewJob(&env.org.UID, string(jobdef.JobTypeNotification))
	third.PreviousJobUID = &second.UID

	for _, job := range []*models.Job{first, second, third} {
		jctx := *env.jctx
		jctx.Job = job
		r.Equal(first.UID, rootJobUID(ctx, &jctx))
	}

	r.Empty(rootJobUID(ctx, &jobdef.JobContext{}))
}
