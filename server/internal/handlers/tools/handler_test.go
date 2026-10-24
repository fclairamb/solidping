package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/analytics"
	"github.com/fclairamb/solidping/server/internal/config"
)

func TestDailyCounterCapsRunsPerDay(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	counter := &dailyCounter{counts: map[string]int{}}
	day := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)

	for i := range RunsPerDay {
		count, ok := counter.add("ip-a", day)
		r.True(ok, "run %d", i+1)
		r.Equal(i+1, count)
	}

	_, ok := counter.add("ip-a", day)
	r.False(ok, "run 101 of the day is refused")

	_, ok = counter.add("ip-b", day)
	r.True(ok, "another caller has its own allowance")

	_, ok = counter.add("ip-a", day.Add(24*time.Hour))
	r.True(ok, "the next UTC day starts over")
	r.Len(counter.counts, 1, "yesterday's counters are dropped")
}

func TestHandlerRefusesThe101stRunOfTheDay(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &config.Config{Tools: config.ToolsConfig{Enabled: true}}
	handler := NewHandler(cfg, newTestService(fakeDNS(nil)), func(*http.Request) string { return "203.0.113.7" })

	now := time.Now().UTC()
	for range RunsPerDay {
		_, ok := handler.daily.add(handler.hashIP("203.0.113.7"), now)
		r.True(ok)
	}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/tools/run",
		strings.NewReader(`{"type":"dns","target":"acme.com"}`))
	r.NoError(handler.Run(recorder, req))

	r.Equal(http.StatusTooManyRequests, recorder.Code)
	r.NotEmpty(recorder.Header().Get("Retry-After"))

	var body struct {
		Code string `json:"code"`
	}
	r.NoError(json.Unmarshal(recorder.Body.Bytes(), &body))
	r.Equal("RATE_LIMITED", body.Code)
}

func TestHandlerMapsRunErrorsToCodes(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Tools: config.ToolsConfig{Enabled: true}}
	svc := newTestService(fakeDNS(map[string][]string{"private.test": {"10.1.2.3"}}))
	handler := NewHandler(cfg, svc, func(*http.Request) string { return "198.51.100.1" })

	for body, want := range map[string]struct {
		status int
		code   string
	}{
		`not json`:                                 {http.StatusBadRequest, "VALIDATION_ERROR"},
		`{"type":"tcp","target":"acme.com:0"}`:     {http.StatusBadRequest, "INVALID_TARGET"},
		`{"type":"tcp","target":"private.test:5"}`: {http.StatusForbidden, "TARGET_NOT_ALLOWED"},
		`{"type":"dns","target":"private.test"}`:   {http.StatusForbidden, "TARGET_NOT_ALLOWED"},
		`{"type":"dns","target":"localhost"}`:      {http.StatusForbidden, "TARGET_NOT_ALLOWED"},
		`{"type":"tcp","target":"nowhere.test:5"}`: {http.StatusUnprocessableEntity, "RESOLVE_FAILED"},
	} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/tools/run", strings.NewReader(body))
		require.NoError(t, handler.Run(recorder, req))
		require.Equal(t, want.status, recorder.Code, body)
		require.Contains(t, recorder.Body.String(), `"code":"`+want.code+`"`, body)
	}
}

func TestBusyAnswers503WithRetryAfter(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &config.Config{Tools: config.ToolsConfig{Enabled: true}}
	svc := newTestService(fakeDNS(nil))

	for range MaxConcurrentRuns {
		svc.slots <- struct{}{}
	}

	handler := NewHandler(cfg, svc, func(*http.Request) string { return "198.51.100.2" })
	recorder := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/tools/run",
		strings.NewReader(`{"type":"dns","target":"acme.com"}`))
	r.NoError(handler.Run(recorder, req))

	r.Equal(http.StatusServiceUnavailable, recorder.Code)
	r.Equal("5", recorder.Header().Get("Retry-After"))
	r.Contains(recorder.Body.String(), `"code":"SERVICE_BUSY"`)
}

type recordingAnalytics struct {
	mu     sync.Mutex
	events []analytics.Event
}

func (c *recordingAnalytics) Capture(_ context.Context, event analytics.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.events = append(c.events, event)
}

func (c *recordingAnalytics) Enabled() bool               { return true }
func (c *recordingAnalytics) Close(context.Context) error { return nil }

// The tool_run event carries type, status and cached, never the target.
//
//nolint:paralleltest // swaps the process-wide analytics client
func TestRunEmitsAnAnonymousAnalyticsEvent(t *testing.T) {
	r := require.New(t)

	recorder := &recordingAnalytics{}
	analytics.SetDefault(recorder)
	t.Cleanup(func() { analytics.SetDefault(nil) })

	cfg := &config.Config{Tools: config.ToolsConfig{Enabled: true}}
	svc := newTestService(fakeDNS(map[string][]string{"acme.com": {"8.8.4.4"}}))
	handler := NewHandler(cfg, svc, func(*http.Request) string { return "198.51.100.3" })

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/tools/run",
		strings.NewReader(`{"type":"dns","target":"acme.com"}`))
	r.NoError(handler.Run(httptest.NewRecorder(), req))

	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	r.Len(recorder.events, 1)
	r.Equal(EventToolRun, recorder.events[0].Name)
	r.Equal(map[string]any{"type": "dns", "status": "up", "cached": false}, recorder.events[0].Properties)
	r.Empty(recorder.events[0].OrgUID)
}

func TestIPHashNeverCarriesTheRawIP(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	handler := NewHandler(&config.Config{}, newTestService(fakeDNS(nil)), nil)

	hash := handler.hashIP("203.0.113.7")
	r.NotContains(hash, "203.0.113.7")
	r.Len(hash, 2*ipHashBytes)
	r.Equal(hash, handler.hashIP("203.0.113.7"), "stable within one process")
	r.NotEqual(hash, handler.hashIP("203.0.113.8"))
}
