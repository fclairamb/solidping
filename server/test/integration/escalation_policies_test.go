package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// authedRequestWithBody issues a request with a JSON body and the test PAT.
func authedRequestWithBody(t *testing.T, ts *TestServer, method, path string, body any) (int, map[string]any) {
	t.Helper()

	raw, err := json.Marshal(body)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(t.Context(), method, ts.HTTPServer.URL+path, bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer pat_test")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var out map[string]any
	if len(data) > 0 {
		require.NoError(t, json.Unmarshal(data, &out))
	}

	return resp.StatusCode, out
}

// createEscalationPolicyViaAPI creates a policy with empty steps and returns its slug.
func createEscalationPolicyViaAPI(t *testing.T, ts *TestServer, slug, name string) {
	t.Helper()

	status, body := authedRequestWithBody(t, ts, http.MethodPost,
		"/api/v1/orgs/"+TestOrgSlug+"/escalation-policies",
		map[string]any{
			"slug":      slug,
			"name":      name,
			"repeatMax": 0,
			"steps":     []any{},
		},
	)
	require.Equal(t, http.StatusCreated, status, "create policy: body=%v", body)
}

// createConnectionViaAPI creates a webhook notification channel and returns its UID.
func createConnectionViaAPI(t *testing.T, ts *TestServer) string {
	t.Helper()

	status, body := authedRequestWithBody(t, ts, http.MethodPost,
		"/api/v1/orgs/"+TestOrgSlug+"/connections",
		map[string]any{
			"name": "E2E Webhook",
			"type": "webhook",
			"settings": map[string]any{
				"url": "https://example.com/hook",
			},
		},
	)
	require.Equal(t, http.StatusCreated, status, "create connection: body=%v", body)
	uid, _ := body["uid"].(string)
	require.NotEmpty(t, uid)

	return uid
}

// createOnCallScheduleViaAPI creates a weekly on-call schedule and returns its UID.
func createOnCallScheduleViaAPI(t *testing.T, ts *TestServer, slug string) string {
	t.Helper()

	status, body := authedRequestWithBody(t, ts, http.MethodPost,
		"/api/v1/orgs/"+TestOrgSlug+"/on-call-schedules",
		map[string]any{
			"name":           "E2E Schedule",
			"slug":           slug,
			"timezone":       "UTC",
			"rotationType":   "weekly",
			"handoffTime":    "09:00",
			"handoffWeekday": 1,
			"startAt":        time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
			"userUids":       []string{},
		},
	)
	require.Equal(t, http.StatusCreated, status, "create schedule: body=%v", body)
	uid, _ := body["uid"].(string)
	require.NotEmpty(t, uid)

	return uid
}

// patchPolicySteps replaces the steps of an escalation policy.
func patchPolicySteps(t *testing.T, ts *TestServer, policySlug string, steps []any) {
	t.Helper()

	status, body := authedRequestWithBody(t, ts, http.MethodPatch,
		"/api/v1/orgs/"+TestOrgSlug+"/escalation-policies/"+policySlug,
		map[string]any{"steps": steps},
	)
	require.Equal(t, http.StatusOK, status, "patch policy steps: body=%v", body)
}

// getFirstTargetUID fetches the policy and returns the targetUid of the first
// target of the first step.
func getFirstTargetUID(t *testing.T, ts *TestServer, policySlug string) string {
	t.Helper()

	status, raw := authedRequest(t, ts, http.MethodGet,
		"/api/v1/orgs/"+TestOrgSlug+"/escalation-policies/"+policySlug)
	require.Equal(t, http.StatusOK, status)

	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))

	steps, _ := body["steps"].([]any)
	require.NotEmpty(t, steps, "expected at least one step")
	step, _ := steps[0].(map[string]any)
	targets, _ := step["targets"].([]any)
	require.NotEmpty(t, targets, "expected at least one target")
	target, _ := targets[0].(map[string]any)
	uid, _ := target["targetUid"].(string)

	return uid
}

// TestEscalationPolicyTargetUIDRoundTrip verifies that targetUid survives a
// PATCH → GET round-trip for each target type. This pins the fix for the
// json:"uid" vs json:"targetUid" mismatch in handler.go.
func TestEscalationPolicyTargetUIDRoundTrip(t *testing.T) {
	t.Parallel()

	ts := NewTestServer(t)
	t.Cleanup(ts.Close)

	// Seed prerequisites.
	connectionUID := createConnectionViaAPI(t, ts)
	scheduleUID := createOnCallScheduleViaAPI(t, ts, "e2e-sched")
	// Test user is created by NewTestServer with this UID.
	userUID := "10000000-0000-0000-0000-000000000002"

	createEscalationPolicyViaAPI(t, ts, "e2e-round-trip", "E2E Round Trip")

	cases := []struct {
		name      string
		targetTyp string
		targetUID string
	}{
		{"connection", "connection", connectionUID},
		{"user", "user", userUID},
		{"schedule", "schedule", scheduleUID},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)

			patchPolicySteps(t, ts, "e2e-round-trip", []any{
				map[string]any{
					"delayMinutes": 0,
					"targets": []any{
						map[string]any{
							"type":      tc.targetTyp,
							"targetUid": tc.targetUID,
						},
					},
				},
			})

			got := getFirstTargetUID(t, ts, "e2e-round-trip")
			r.Equal(tc.targetUID, got, "targetUid must round-trip for type %q", tc.targetTyp)
		})
	}

	t.Run("all_admins", func(t *testing.T) {
		r := require.New(t)

		patchPolicySteps(t, ts, "e2e-round-trip", []any{
			map[string]any{
				"delayMinutes": 0,
				"targets": []any{
					map[string]any{"type": "all_admins"},
				},
			},
		})

		// all_admins has no targetUid — the key must be absent or empty.
		got := getFirstTargetUID(t, ts, "e2e-round-trip")
		r.Empty(got, "all_admins must not produce a targetUid")
	})
}
