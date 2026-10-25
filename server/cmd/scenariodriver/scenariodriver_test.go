package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildAssertPredicate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		assert  map[string]any
		payload map[string]any
		want    bool
	}{
		{"event type match", map[string]any{"event_type": "a"}, map[string]any{"eventType": "a"}, true},
		{"event type mismatch", map[string]any{"event_type": "a"}, map[string]any{"eventType": "b"}, false},
		{"resolved wanted and present", map[string]any{"resolved_at_present": true},
			map[string]any{"resolvedAt": "2026-01-01"}, true},
		{"resolved wanted but empty", map[string]any{"resolved_at_present": true},
			map[string]any{"resolvedAt": ""}, false},
		{"resolved unwanted but present", map[string]any{"resolved_at_present": false},
			map[string]any{"resolvedAt": "x"}, false},
		{"resolved non-bool is ignored", map[string]any{"resolved_at_present": "yes"}, map[string]any{}, true},
		{"generic field", map[string]any{"severity": "high"}, map[string]any{"severity": "high"}, true},
		{"generic field mismatch", map[string]any{"severity": "high"}, map[string]any{"severity": "low"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, buildAssertPredicate(tt.assert)(tt.payload))
		})
	}
}

func TestMatchers(t *testing.T) {
	t.Parallel()

	assert.True(t, matchEventType("x")(map[string]any{"eventType": "x"}))
	assert.False(t, matchEventType("x")(map[string]any{"eventType": 3}))
	assert.True(t, matchResolvedAtPresent()(map[string]any{"resolvedAt": "t"}))
	assert.False(t, matchResolvedAtPresent()(map[string]any{"resolvedAt": nil}))
	assert.False(t, matchResolvedAtPresent()(map[string]any{}))
}

func TestDescribeAssert(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "incident.created", describeAssert(map[string]any{"event_type": "incident.created"}))
	assert.Equal(t, "map[a:1]", describeAssert(map[string]any{"a": 1}))
}

func postHook(t *testing.T, r *WebhookReceiver, path, body string) int {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+r.Addr()+path,
		strings.NewReader(body))
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	return resp.StatusCode
}

func TestWebhookReceiver(t *testing.T) {
	t.Parallel()

	r, err := NewWebhookReceiver("127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close(context.Background()) })

	t.Run("json payload is delivered to waiter", func(t *testing.T) {
		t.Parallel()

		done := make(chan error, 1)

		go func() {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			done <- r.WaitForEvent(ctx, "c1", matchEventType("x"))
		}()

		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, http.StatusOK, postHook(t, r, "/hook/c1", `{"eventType":"x"}`))
		require.NoError(t, <-done)
	})

	t.Run("already received payload matches immediately", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, http.StatusOK, postHook(t, r, "/hook/c2/", `{"eventType":"y"}`))
		require.NoError(t, r.WaitForEvent(t.Context(), "c2", matchEventType("y")))
	})

	t.Run("non json body is kept raw", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, http.StatusOK, postHook(t, r, "/hook/c3", `not json`))
		require.NoError(t, r.WaitForEvent(t.Context(), "c3", func(m map[string]any) bool {
			return m["_rawBody"] == "not json"
		}))
	})

	t.Run("empty body is an empty payload", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, http.StatusOK, postHook(t, r, "/hook/c4", ""))
		require.NoError(t, r.WaitForEvent(t.Context(), "c4", func(m map[string]any) bool { return len(m) == 0 }))
	})

	t.Run("missing channel id is rejected", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, http.StatusBadRequest, postHook(t, r, "/hook/", "{}"))
	})

	t.Run("non post is rejected", func(t *testing.T) {
		t.Parallel()

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+r.Addr()+"/hook/c1", nil)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	})

	t.Run("wait times out without match", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		require.ErrorIs(t, r.WaitForEvent(ctx, "never", matchEventType("z")), context.DeadlineExceeded)
	})

	t.Run("wait with already canceled context", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.ErrorIs(t, r.WaitForEvent(ctx, "never2", matchEventType("z")), context.Canceled)
	})
}

func TestNewWebhookReceiverBadAddress(t *testing.T) {
	t.Parallel()

	_, err := NewWebhookReceiver("256.256.256.256:99999")
	require.Error(t, err)
}

func TestAssertWebhook(t *testing.T) {
	t.Parallel()

	r, err := NewWebhookReceiver("127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close(context.Background()) })

	require.Equal(t, http.StatusOK, postHook(t, r, "/hook/a", `{"eventType":"ok"}`))
	require.NoError(t, assertWebhook(t.Context(), r, "a", "ok", matchEventType("ok"), time.Second))

	err = assertWebhook(t.Context(), r, "b", "expected thing", matchEventType("ok"), 50*time.Millisecond)
	require.ErrorContains(t, err, "no webhook received within 50ms: expected thing")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = assertWebhook(ctx, r, "c", "d", matchEventType("ok"), 0)
	require.Error(t, err)
}

func TestOutputPrinterAndJUnit(t *testing.T) {
	t.Parallel()

	steps := []stepResult{
		{Name: "one", Duration: time.Second},
		{Name: "two", Duration: 2 * time.Second, Err: assert.AnError},
	}

	var buf bytes.Buffer
	NewOutputPrinter(&buf).PrintScenario("sc", 3*time.Second, assert.AnError, steps)
	out := buf.String()
	assert.Contains(t, out, "✗ sc  3.0s  FAILED")
	assert.Contains(t, out, "✓ one")
	assert.Contains(t, out, "✗ two")

	buf.Reset()
	NewOutputPrinter(&buf).PrintScenario("sc", time.Second, nil, steps[:1])
	assert.Contains(t, buf.String(), "✓ sc  1.0s\n")

	path := filepath.Join(t.TempDir(), "out.xml")
	require.NoError(t, WriteJUnit(path, "sc", steps, 3*time.Second))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `<testsuite name="sc" tests="2" failures="1"`)
	assert.Contains(t, string(raw), `<failure message="`)

	require.Error(t, WriteJUnit(filepath.Join(t.TempDir(), "missing", "out.xml"), "sc", steps, 0))
}

func TestAPIClientDo(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			assert.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"a":1}`))
		case "/problem":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"title":"Bad","detail":"nope"}`))
		case "/plain":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`boom`))
		case "/garbage":
			_, _ = w.Write([]byte(`{`))
		}
	}))
	t.Cleanup(srv.Close)

	c := newAPIClient(srv.URL, "tok")

	var out map[string]any

	status, err := c.do(t.Context(), http.MethodPost, "/ok", map[string]any{"x": 1}, &out)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	assert.InDelta(t, 1, out["a"], 0)

	_, err = c.do(t.Context(), http.MethodGet, "/problem", nil, nil)
	require.ErrorContains(t, err, "API error 400: Bad")

	_, err = c.do(t.Context(), http.MethodGet, "/plain", nil, nil)
	require.ErrorContains(t, err, "API error 500: boom")

	_, err = c.do(t.Context(), http.MethodGet, "/garbage", nil, &out)
	require.ErrorContains(t, err, "decode response")

	_, err = c.do(t.Context(), http.MethodPost, "/ok", func() {}, nil)
	require.ErrorContains(t, err, "marshal body")

	_, err = c.do(t.Context(), http.MethodGet, "/ok", nil, nil)
	require.NoError(t, err)

	_, err = newAPIClient("http://127.0.0.1:1", "").do(t.Context(), http.MethodGet, "/x", nil, nil)
	require.ErrorContains(t, err, "do request")

	_, err = newAPIClient("http://bad host", "").do(t.Context(), http.MethodGet, "/x", nil, nil)
	require.ErrorContains(t, err, "build request")
}

// fakeAPI mimics the SolidPing endpoints the scenario driver touches and
// forwards heartbeats as webhooks to the registered channel URL.
type fakeAPI struct {
	mu         sync.Mutex
	channelURL string
	deleted    []string
	failOn     string
	incidents  int
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failOn != "" && strings.Contains(r.Method+" "+r.URL.Path, f.failOn) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"title":"forced"}`))

		return
	}

	path := r.URL.Path

	switch {
	case r.Method == http.MethodDelete:
		f.deleted = append(f.deleted, path)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/checks"):
		_ = json.NewEncoder(w).Encode(map[string]any{
			"uid": "check-uid", "slug": "hb", "config": map[string]any{"token": "tok"},
		})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/channels"):
		var body struct {
			Settings struct {
				URL string `json:"url"`
			} `json:"settings"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.channelURL = body.Settings.URL
		_ = json.NewEncoder(w).Encode(map[string]any{"uid": "chan-uid"})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/escalation-policies"):
		_ = json.NewEncoder(w).Encode(map[string]any{"uid": "pol-uid"})
	case r.Method == http.MethodPatch:
		_ = json.NewEncoder(w).Encode(map[string]any{})
	case strings.Contains(path, "/heartbeat/"):
		event := "incident.created"
		payload := map[string]any{"eventType": event}

		if r.URL.Query().Get("status") == "up" {
			payload = map[string]any{"eventType": "incident.resolved", "resolvedAt": "now"}
		}

		raw, _ := json.Marshal(payload)
		resp, err := http.Post(f.channelURL, "application/json", bytes.NewReader(raw)) //nolint:noctx
		if err == nil {
			_ = resp.Body.Close()
		}

		w.WriteHeader(http.StatusOK)
	case strings.HasSuffix(path, "/incidents"):
		data := []any{}
		for range f.incidents {
			data = append(data, map[string]any{"uid": "i"})
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	return addr
}

const fullScenario = `
name: full
timeout: 20s
cleanup: true
steps:
  - kind: create_heartbeat_check
    id: chk
    slug: "hb"
  - kind: create_webhook_channel
    id: ch
    url: "{{.ReceiverURL}}/hook/ch"
  - kind: create_escalation_policy
    id: pol
    repeat_max: 1
    repeat_after_seconds: 5
    steps:
      - delay_seconds: 0
        targets:
          - type: connection
            channel_id: ch
          - type: all_admins
  - kind: assign_policy_to_check
    check_id: chk
    policy_id: pol
  - kind: assert_no_webhook
    channel_id: ch
    duration: 100ms
  - kind: send_heartbeat
    check_id: chk
    status: down
  - kind: expect_webhook
    channel_id: ch
    timeout: 5s
    assert:
      event_type: incident.created
  - kind: assert_incident_open
    check_id: chk
  - kind: wait
    duration: 10ms
  - kind: send_heartbeat
    check_id: chk
    status: up
  - kind: expect_webhook
    channel_id: ch
    timeout: 5s
    assert:
      event_type: incident.resolved
      resolved_at_present: true
`

func writeScenario(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "s.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	return path
}

func TestRunScenarioEndToEnd(t *testing.T) {
	t.Parallel()

	api := &fakeAPI{incidents: 1}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	listen := freeAddr(t)
	junit := filepath.Join(t.TempDir(), "junit.xml")

	err := RunScenario(t.Context(), RunConfig{
		ServerURL:    srv.URL,
		Org:          "acme",
		Token:        "t",
		ScenarioFile: writeScenario(t, fullScenario),
		Listen:       listen,
		PublicURL:    "http://" + listen,
		JUnitPath:    junit,
	})
	require.NoError(t, err)

	api.mu.Lock()
	defer api.mu.Unlock()

	assert.Len(t, api.deleted, 3, "check, channel and policy are cleaned up")

	raw, err := os.ReadFile(junit)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "failures=\"0\"")
}

func TestRunScenarioFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		failOn  string
		yaml    string
		wantErr string
	}{
		{"create check fails", "POST /api/v1/orgs/acme/checks", fullScenario, "create heartbeat check"},
		{"create channel fails", "/channels", fullScenario, "create webhook channel"},
		{"create policy fails", "/escalation-policies", fullScenario, "create escalation policy"},
		{"assign fails", "PATCH", fullScenario, "assign policy to check"},
		{"heartbeat fails", "/heartbeat/", fullScenario, "send heartbeat"},
		{"incidents list fails", "/incidents", fullScenario, "list incidents"},
		{"unknown kind", "", "name: x\nsteps:\n  - kind: nope\n", "unknown step kind"},
		{"unknown channel in policy", "",
			"name: x\nsteps:\n  - kind: create_escalation_policy\n    steps:\n      - targets:\n          - type: connection\n            channel_id: ghost\n",
			"resolve channel_id"},
		{"unknown check in assign", "", "name: x\nsteps:\n  - kind: assign_policy_to_check\n    check_id: a\n    policy_id: b\n",
			"resolve check_id"},
		{"unknown policy in assign", "",
			"name: x\nsteps:\n  - kind: create_heartbeat_check\n    id: a\n  - kind: assign_policy_to_check\n    check_id: a\n    policy_id: b\n",
			"resolve policy_id"},
		{"heartbeat without check", "", "name: x\nsteps:\n  - kind: send_heartbeat\n    check_id: a\n", "resolve check_id"},
		{"heartbeat without token", "",
			"name: x\nsteps:\n  - kind: create_webhook_channel\n    id: a\n    url: http://x\n  - kind: send_heartbeat\n    check_id: a\n",
			"no heartbeat token"},
		{"incident check unknown", "", "name: x\nsteps:\n  - kind: assert_incident_open\n    check_id: a\n", "resolve check_id"},
		{"expect webhook times out", "",
			"name: x\nsteps:\n  - kind: expect_webhook\n    channel_id: c\n    timeout: 50ms\n    assert:\n      event_type: e\n",
			"no webhook received"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := &fakeAPI{failOn: tt.failOn}
			srv := httptest.NewServer(api)
			t.Cleanup(srv.Close)

			listen := freeAddr(t)
			err := RunScenario(t.Context(), RunConfig{
				ServerURL:     srv.URL,
				Org:           "acme",
				ScenarioFile:  writeScenario(t, tt.yaml),
				Listen:        listen,
				PublicURL:     "http://" + listen,
				GlobalTimeout: 20 * time.Second,
			})
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestStepAssertNoWebhookFailsWhenWebhookArrives(t *testing.T) {
	t.Parallel()

	r, err := NewWebhookReceiver("127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	require.Equal(t, http.StatusOK, postHook(t, r, "/hook/c", `{}`))

	runner := &scenarioRunner{receiver: r, ids: map[string]resolvedID{}}
	_, err = runner.stepAssertNoWebhook(t.Context(), StepYAML{ChannelID: "c", Duration: time.Second})
	require.ErrorContains(t, err, "unexpected webhook received")
}

func TestRunScenarioSetupErrors(t *testing.T) {
	t.Parallel()

	t.Run("listen address in use", func(t *testing.T) {
		t.Parallel()

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = ln.Close() })

		err = RunScenario(t.Context(), RunConfig{Listen: ln.Addr().String()})
		require.ErrorContains(t, err, "start webhook receiver")
	})

	t.Run("scenario file missing", func(t *testing.T) {
		t.Parallel()

		err := RunScenario(t.Context(), RunConfig{Listen: freeAddr(t), ScenarioFile: "/nonexistent.yaml"})
		require.ErrorContains(t, err, "load scenario")
	})
}

func TestLoadScenarioErrors(t *testing.T) {
	t.Parallel()

	_, err := loadScenario(writeScenario(t, "{{ .Broken"), TemplateData{})
	require.ErrorContains(t, err, "parse template")

	_, err = loadScenario(writeScenario(t, "{{ .Nope.Deeper }}"), TemplateData{})
	require.ErrorContains(t, err, "expand template")

	_, err = loadScenario(writeScenario(t, "steps: [unterminated"), TemplateData{})
	require.ErrorContains(t, err, "unmarshal")

	sc, err := loadScenario(writeScenario(t, "name: n-{{.OrgSlug}}\n"), TemplateData{OrgSlug: "acme"})
	require.NoError(t, err)
	assert.Equal(t, "n-acme", sc.Name)
}

func TestBuiltInScenariosParse(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("scenarios/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	for _, f := range files {
		sc, err := loadScenario(f, TemplateData{ReceiverURL: "http://r", OrgSlug: "acme", Timestamp: 1})
		require.NoError(t, err, f)
		assert.NotEmpty(t, sc.Steps, f)
	}
}

func TestCommands(t *testing.T) {
	t.Parallel()

	list := listCommand()
	require.NoError(t, list.Action(t.Context(), list))

	run := runCommand()
	assert.Equal(t, "run", run.Name)

	err := run.Run(t.Context(), []string{"run"})
	require.ErrorContains(t, err, "--token is required")

	err = run.Run(t.Context(), []string{"run", "--token", "t"})
	require.ErrorContains(t, err, "--scenario is required")

	// Default public URL is derived from the listen address, then the run fails on the missing file.
	err = run.Run(t.Context(), []string{"run", "--token", "t", "--scenario", "/nonexistent.yaml",
		"--listen", freeAddr(t)})
	require.ErrorContains(t, err, "load scenario")
}
