package scenario

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// StepDef describes one step of an escalation policy to create.
type StepDef struct {
	DelayMinutes int
	Targets      []TargetDef
}

// TargetDef describes one target within an escalation policy step.
type TargetDef struct {
	Type      string // "connection", "user", "schedule", "all_admins"
	TargetUID string // Required for connection, user, schedule targets
}

// CreateHeartbeatCheck creates a heartbeat check in the scenario org and
// returns the check UID and its heartbeat token. The check is configured with
// confirmationPeriodSeconds=0 and escalationThreshold=1 so an incident opens
// and escalates on the very first "down" heartbeat.
func CreateHeartbeatCheck(t *testing.T, s *Scenario, slug string) (checkUID, heartbeatToken string) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	confirmPeriod := 0
	recoveryPeriod := 0

	status, body := doJSON(ctx, t, http.MethodPost,
		s.BaseURL+"/api/v1/orgs/"+s.OrgSlug+"/checks",
		&s.Token,
		map[string]any{
			"name":                      slug,
			"slug":                      slug,
			"type":                      "heartbeat",
			"config":                    map[string]any{},
			"confirmationPeriodSeconds": confirmPeriod,
			"recoveryPeriodSeconds":     recoveryPeriod,
		},
	)
	r.Equal(http.StatusCreated, status, "CreateHeartbeatCheck: %v", body)

	checkUID, _ = body["uid"].(string)
	r.NotEmpty(checkUID, "check UID")

	// The heartbeat token lives inside check.config.token.
	cfgRaw, _ := body["config"].(map[string]any)
	heartbeatToken, _ = cfgRaw["token"].(string)
	r.NotEmpty(heartbeatToken, "heartbeat token in check config")

	return checkUID, heartbeatToken
}

// CreateWebhookChannel creates a webhook notification channel in the scenario
// org and returns its UID.
func CreateWebhookChannel(t *testing.T, s *Scenario, receiverURL string) string {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	status, body := doJSON(ctx, t, http.MethodPost,
		s.BaseURL+"/api/v1/orgs/"+s.OrgSlug+"/channels",
		&s.Token,
		map[string]any{
			"name": "e2e-webhook-" + randOrgSlug(),
			"type": "webhook",
			"settings": map[string]any{
				"url": receiverURL,
			},
		},
	)
	r.Equal(http.StatusCreated, status, "CreateWebhookChannel: %v", body)

	uid, _ := body["uid"].(string)
	r.NotEmpty(uid, "channel UID")

	return uid
}

// CreateEscalationPolicy creates an escalation policy with the given steps and
// returns its UID.
func CreateEscalationPolicy(t *testing.T, s *Scenario, policySlug string, steps []StepDef) string {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	// Build steps payload.
	stepsPayload := make([]any, 0, len(steps))
	for _, step := range steps {
		targets := make([]any, 0, len(step.Targets))
		for _, tgt := range step.Targets {
			tgtMap := map[string]any{"type": tgt.Type}
			if tgt.TargetUID != "" {
				tgtMap["targetUid"] = tgt.TargetUID
			}

			targets = append(targets, tgtMap)
		}

		stepsPayload = append(stepsPayload, map[string]any{
			"delayMinutes": step.DelayMinutes,
			"targets":      targets,
		})
	}

	status, body := doJSON(ctx, t, http.MethodPost,
		s.BaseURL+"/api/v1/orgs/"+s.OrgSlug+"/escalation-policies",
		&s.Token,
		map[string]any{
			"slug":      policySlug,
			"name":      "E2E Policy " + policySlug,
			"repeatMax": 0,
			"steps":     stepsPayload,
		},
	)
	r.Equal(http.StatusCreated, status, "CreateEscalationPolicy: %v", body)

	uid, _ := body["uid"].(string)
	r.NotEmpty(uid, "policy UID")

	return uid
}

// BindChannelToCheck binds a notification channel to a check so that
// incident.created and incident.resolved events are delivered via
// queueNotifications (the direct check→channel path, separate from the
// escalation policy path).
func BindChannelToCheck(t *testing.T, s *Scenario, checkSlug, channelUID string) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	status, body := doJSON(ctx, t, http.MethodPost,
		s.BaseURL+"/api/v1/orgs/"+s.OrgSlug+"/checks/"+checkSlug+"/channels/"+channelUID,
		&s.Token,
		nil,
	)
	r.Equal(http.StatusCreated, status, "BindChannelToCheck: %v", body)
}

// AssignPolicyToCheck assigns an escalation policy to a check by slug.
func AssignPolicyToCheck(t *testing.T, s *Scenario, checkSlug, policyUID string) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	status, body := doJSON(ctx, t, http.MethodPatch,
		s.BaseURL+"/api/v1/orgs/"+s.OrgSlug+"/checks/"+checkSlug,
		&s.Token,
		map[string]any{
			"escalationPolicyUid": policyUID,
		},
	)
	r.Equal(http.StatusOK, status, "AssignPolicyToCheck: %v", body)
}

// SendHeartbeat sends a heartbeat ping to the server with the given status
// ("up" or "down").
func SendHeartbeat(t *testing.T, s *Scenario, checkSlug, token, status string) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	url := s.BaseURL + "/api/v1/heartbeat/" + s.OrgSlug + "/" + checkSlug +
		"?token=" + token + "&status=" + status

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	r.NoError(err)

	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)

	_ = resp.Body.Close()

	r.Equal(http.StatusOK, resp.StatusCode, "SendHeartbeat(%s, %s)", checkSlug, status)
}

// CreateOnCallSchedule creates a weekly on-call schedule starting 24h ago
// (so the test user is always on call) and returns its UID.
func CreateOnCallSchedule(t *testing.T, s *Scenario, slug string, userUIDs []string) string {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	status, body := doJSON(ctx, t, http.MethodPost,
		s.BaseURL+"/api/v1/orgs/"+s.OrgSlug+"/on-call-schedules",
		&s.Token,
		map[string]any{
			"name":           "E2E Schedule " + slug,
			"slug":           slug,
			"timezone":       "UTC",
			"rotationType":   "weekly",
			"handoffTime":    "00:00",
			"handoffWeekday": 1,
			"startAt":        time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339),
			"userUids":       userUIDs,
		},
	)
	r.Equal(http.StatusCreated, status, "CreateOnCallSchedule: %v", body)

	uid, _ := body["uid"].(string)
	r.NotEmpty(uid, "schedule UID")

	return uid
}

// waitForWebhookEvent is a helper for tests that wraps WaitForWebhook with a
// standard timeout and fails the test on timeout.
func waitForWebhookEvent(t *testing.T, wc *WebhookCollector, eventType string) map[string]any {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	payload, err := wc.WaitForWebhook(ctx, func(m map[string]any) bool {
		et, _ := m["eventType"].(string)
		return et == eventType
	})

	require.NoError(t, err, "timed out waiting for webhook event %q", eventType)

	return payload
}
