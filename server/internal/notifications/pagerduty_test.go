package notifications

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// pagerdutyFake captures every request posted by the sender and answers with
// a caller-configured status code, mimicking the Events API v2 endpoint
// (which answers 202 Accepted on success).
type pagerdutyFake struct {
	mu       sync.Mutex
	requests int
	bodies   [][]byte
	status   int
}

func newPagerdutyFake(t *testing.T, status int) (*pagerdutyFake, string) {
	t.Helper()

	f := &pagerdutyFake{status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		f.mu.Lock()
		f.requests++
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()

		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(`{"status":"success"}`))
	}))
	t.Cleanup(srv.Close)

	return f, srv.URL
}

func (f *pagerdutyFake) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.requests
}

func (f *pagerdutyFake) lastEvent(t *testing.T) map[string]any {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	require.NotEmpty(t, f.bodies)

	var event map[string]any

	err := json.Unmarshal(f.bodies[len(f.bodies)-1], &event)
	require.NoError(t, err)

	return event
}

func pagerdutyCheck() *models.Check {
	name := "API health"

	return &models.Check{UID: "chk-1", Name: &name, Type: "http"}
}

func pagerdutyPayload(event string) *Payload {
	return &Payload{
		EventType: event,
		Incident: &models.Incident{
			UID:          "018e4a2b-incident",
			StartedAt:    time.Now().Add(-5 * time.Minute),
			FailureCount: 3,
			RelapseCount: 2,
			Details: models.JSONMap{
				"failure_reason": "upstream unreachable",
				"first_result":   map[string]any{"status": resultStatusStrDown},
			},
		},
		Check:   pagerdutyCheck(),
		OrgSlug: "acme",
		Integration: &models.Integration{
			UID: "chan-1", OrganizationUID: "org-1",
			Type:     models.ConnectionTypePagerduty,
			Settings: models.JSONMap{"routing_key": "test-routing-key"},
		},
	}
}

func TestPagerDutySender_TriggerOnCreated(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, url := newPagerdutyFake(t, http.StatusAccepted)
	payload := pagerdutyPayload(eventTypeIncidentCreated)

	sender := &PagerDutySender{EventsURL: url}
	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.NoError(err)

	r.Equal(1, fake.requestCount())

	event := fake.lastEvent(t)
	r.Equal("test-routing-key", event[pagerdutyFieldRoutingKey])
	r.Equal(pagerdutyEventActionTrigger, event["event_action"])
	r.Equal(payload.Incident.UID, event["dedup_key"])

	pdPayload, ok := event["payload"].(map[string]any)
	r.True(ok, "payload must be an object")
	r.Equal("[DOWN] API health", pdPayload["summary"])
	r.Equal(pagerdutySeverityCritical, pdPayload["severity"])
}

func TestPagerDutySender_TriggerOnReopened(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, url := newPagerdutyFake(t, http.StatusAccepted)
	payload := pagerdutyPayload(eventTypeIncidentReopened)

	sender := &PagerDutySender{EventsURL: url}
	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.NoError(err)

	event := fake.lastEvent(t)
	r.Equal(pagerdutyEventActionTrigger, event["event_action"])
	r.Equal(payload.Incident.UID, event["dedup_key"])

	pdPayload, ok := event["payload"].(map[string]any)
	r.True(ok)
	r.Equal("[REOPENED] API health (relapse #2)", pdPayload["summary"])
}

// TestPagerDutySender_DedupKeyPropagation is the correlation regression:
// trigger and resolve for the SAME incident must carry the SAME dedup_key so
// PagerDuty treats them as one incident's lifecycle rather than two.
func TestPagerDutySender_DedupKeyPropagation(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, url := newPagerdutyFake(t, http.StatusAccepted)

	created := pagerdutyPayload(eventTypeIncidentCreated)
	sender := &PagerDutySender{EventsURL: url}
	r.NoError(sender.Send(context.Background(), &jobdef.JobContext{}, created))
	triggerEvent := fake.lastEvent(t)

	resolved := pagerdutyPayload(eventTypeIncidentResolved)
	r.NoError(sender.Send(context.Background(), &jobdef.JobContext{}, resolved))
	resolveEvent := fake.lastEvent(t)

	r.Equal(created.Incident.UID, triggerEvent["dedup_key"])
	r.Equal(resolved.Incident.UID, resolveEvent["dedup_key"])
	r.Equal(triggerEvent["dedup_key"], resolveEvent["dedup_key"])
	r.Equal(pagerdutyEventActionResolve, resolveEvent["event_action"])
}

func TestPagerDutySender_SeverityMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		firstResult  map[string]any
		wantSeverity string
	}{
		{
			name: "down-is-critical", firstResult: map[string]any{"status": resultStatusStrDown},
			wantSeverity: pagerdutySeverityCritical,
		},
		{
			name: "timeout-is-error", firstResult: map[string]any{"status": resultStatusStrTimeout},
			wantSeverity: pagerdutySeverityError,
		},
		{
			name: "error-is-error", firstResult: map[string]any{"status": resultStatusStrError},
			wantSeverity: pagerdutySeverityError,
		},
		{
			name: "warning-is-warning", firstResult: map[string]any{"status": resultStatusStrWarning},
			wantSeverity: pagerdutySeverityWarning,
		},
		{
			name: "degraded-is-warning", firstResult: map[string]any{"status": resultStatusStrDegraded},
			wantSeverity: pagerdutySeverityWarning,
		},
		{
			name: "unknown-is-info", firstResult: map[string]any{"status": "UP"},
			wantSeverity: pagerdutySeverityInfo,
		},
		{name: "missing-snapshot-is-info", firstResult: nil, wantSeverity: pagerdutySeverityInfo},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			fake, url := newPagerdutyFake(t, http.StatusAccepted)
			payload := pagerdutyPayload(eventTypeIncidentCreated)

			if tt.firstResult == nil {
				delete(payload.Incident.Details, "first_result")
			} else {
				payload.Incident.Details["first_result"] = tt.firstResult
			}

			sender := &PagerDutySender{EventsURL: url}
			err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
			r.NoError(err)

			event := fake.lastEvent(t)
			pdPayload, ok := event["payload"].(map[string]any)
			r.True(ok)
			r.Equal(tt.wantSeverity, pdPayload["severity"])
		})
	}
}

// TestPagerDutySender_LastFailureTakesPriorityOverFirstResult proves a
// relapse's CURRENT status (last_failure) drives severity, not the
// original opening failure (first_result).
func TestPagerDutySender_LastFailureTakesPriorityOverFirstResult(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, url := newPagerdutyFake(t, http.StatusAccepted)
	payload := pagerdutyPayload(eventTypeIncidentReopened)
	payload.Incident.Details["first_result"] = map[string]any{"status": resultStatusStrDown}
	payload.Incident.Details["last_failure"] = map[string]any{"status": resultStatusStrWarning}

	sender := &PagerDutySender{EventsURL: url}
	r.NoError(sender.Send(context.Background(), &jobdef.JobContext{}, payload))

	event := fake.lastEvent(t)
	pdPayload, ok := event["payload"].(map[string]any)
	r.True(ok)
	r.Equal(pagerdutySeverityWarning, pdPayload["severity"])
}

// TestPagerDutySender_CommentAndEscalationSendNothing proves the negative —
// a comment or an escalation must never make an HTTP request, because a
// trigger reusing dedup_key would re-open a resolved incident and the
// Events API has no note concept to fall back to. incidentCreated is the
// positive control in the same test, proving the fake server and sender
// wiring actually work (so a silently-broken sender cannot pass by having
// every case produce zero requests).
func TestPagerDutySender_CommentAndEscalationSendNothing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, url := newPagerdutyFake(t, http.StatusAccepted)
	sender := &PagerDutySender{EventsURL: url}

	// Positive control: incidentCreated MUST hit the server.
	created := pagerdutyPayload(eventTypeIncidentCreated)
	r.NoError(sender.Send(context.Background(), &jobdef.JobContext{}, created))
	r.Equal(1, fake.requestCount(), "positive control: incidentCreated must send")

	// Negative cases: neither must add another request.
	comment := pagerdutyPayload(eventTypeIncidentComment)
	comment.Comment = &CommentInfo{Text: "investigating", AuthorName: "alice"}
	r.NoError(sender.Send(context.Background(), &jobdef.JobContext{}, comment))
	r.Equal(1, fake.requestCount(), "a comment must not send anything to PagerDuty")

	escalated := pagerdutyPayload(eventTypeIncidentEscalated)
	r.NoError(sender.Send(context.Background(), &jobdef.JobContext{}, escalated))
	r.Equal(1, fake.requestCount(), "an escalation must not send anything to PagerDuty")
}

func TestPagerDutySender_MissingRoutingKeyErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := pagerdutyPayload(eventTypeIncidentCreated)
	payload.Integration.Settings = models.JSONMap{}

	sender := &PagerDutySender{}
	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.ErrorIs(err, ErrPagerdutyRoutingKeyNotConfigured)
}

func TestPagerDutySender_NonSuccessStatusErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, url := newPagerdutyFake(t, http.StatusBadRequest)
	payload := pagerdutyPayload(eventTypeIncidentCreated)

	sender := &PagerDutySender{EventsURL: url}
	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.Error(err)
	r.False(jobdef.IsRetryable(err), "400 must not be retryable")
	r.Equal(1, fake.requestCount())
}

func TestPagerDutySender_RateLimitedIsRetryable(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, url := newPagerdutyFake(t, http.StatusTooManyRequests)
	payload := pagerdutyPayload(eventTypeIncidentCreated)

	sender := &PagerDutySender{EventsURL: url}
	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.Error(err)
	r.True(jobdef.IsRetryable(err), "429 must be retryable")
	_ = fake
}

func TestPagerDutySender_ServerErrorIsRetryable(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, url := newPagerdutyFake(t, http.StatusServiceUnavailable)
	payload := pagerdutyPayload(eventTypeIncidentCreated)

	sender := &PagerDutySender{EventsURL: url}
	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.Error(err)
	r.True(jobdef.IsRetryable(err), "503 must be retryable")
	_ = fake
}

func TestPagerDutySender_AcceptedStatusIsSuccess(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, url := newPagerdutyFake(t, http.StatusAccepted)
	payload := pagerdutyPayload(eventTypeIncidentCreated)

	sender := &PagerDutySender{EventsURL: url}
	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.NoError(err)
}
