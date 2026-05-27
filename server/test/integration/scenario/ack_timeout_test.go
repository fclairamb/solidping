package scenario

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestAckTimeoutRepeatCycle verifies that a repeat-enabled escalation policy
// fires a second paging cycle when an incident remains unacknowledged, using
// the fake clock to advance time without real sleeps.
//
// Pipeline path exercised:
//
//	heartbeat POST (status=down)
//	  → incidentSvc.ProcessCheckResult
//	  → scheduleEscalationPolicy → cycle 0 step jobs
//	  → escalation step job fires → notification job
//	  → webhook (incident.escalated, cycle 0)
//
//	scenario.Clock.Advance(61 * time.Second) — triggers repeat-cycle schedule
//	  → escalation step job (last step) schedules cycle 1
//	  → cycle 1 step jobs fire immediately (delay=0)
//	  → notification job → webhook (incident.escalated, cycle 1)
func TestAckTimeoutRepeatCycle(t *testing.T) {
	t.Parallel()

	s := NewPostgresScenario(t)
	r := require.New(t)

	// 1. Create a heartbeat check with immediate confirmation and recovery.
	checkUID, hbToken := CreateHeartbeatCheck(t, s, "ack-timeout-check")
	r.NotEmpty(checkUID)
	r.NotEmpty(hbToken)

	// 2. Create a webhook channel pointing at the in-test receiver.
	channelUID := CreateWebhookChannel(t, s, s.WebhookURL)

	// 3. Create an escalation policy: repeat 1 cycle after 1 minute (minimum
	//    granularity in this model), single step with delay=0.
	repeatMax := 1
	repeatAfterSeconds := 60
	policySlug := "ack-timeout-policy"

	ctx := t.Context()
	status, body := doJSON(ctx, t, "POST",
		s.BaseURL+"/api/v1/orgs/"+s.OrgSlug+"/escalation-policies",
		&s.Token,
		map[string]any{
			"slug":               policySlug,
			"name":               "Ack Timeout Policy",
			"repeatMax":          repeatMax,
			"repeatAfterSeconds": repeatAfterSeconds,
			"steps": []any{
				map[string]any{
					"delaySeconds": 0,
					"targets": []any{
						map[string]any{
							"type":      "connection",
							"targetUid": channelUID,
						},
					},
				},
			},
		},
	)
	r.Equal(201, status, "create policy: %v", body)

	policyUID, _ := body["uid"].(string)
	r.NotEmpty(policyUID, "policy UID")

	// 4. Assign policy to check.
	AssignPolicyToCheck(t, s, "ack-timeout-check", policyUID)

	// 5. Send a "down" heartbeat — incident opens, cycle 0 fires.
	SendHeartbeat(t, s, "ack-timeout-check", hbToken, "down")

	// 6. Wait for the first escalation webhook (cycle 0).
	firstEscalated := waitForWebhookEvent(t, s.Webhooks, "incident.escalated")
	r.Equal("incident.escalated", firstEscalated["type"])
	_, chkData := webhookPayloadExtract(firstEscalated)
	r.Equal(checkUID, chkData["uid"])

	// 7. Advance the fake clock past the repeat-after window (1 minute + 1s).
	// The escalation step job schedules cycle 1 startAt = clock.Now() + 1 minute.
	// Advancing 61 seconds past that causes the job scheduler to pick it up.
	s.Clock.Advance(61 * time.Second)

	// 8. Wait for the second distinct incident.escalated event (cycle 1).
	// We use a count-based predicate: wait until there are 2 escalated events.
	secondCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	escalatedCount := 0
	_, err := s.Webhooks.WaitForWebhook(secondCtx, func(m map[string]any) bool {
		et, _ := m["type"].(string)
		if et != "incident.escalated" {
			return false
		}
		escalatedCount++
		// Return true only when we see the second one.
		return escalatedCount >= 2
	})
	r.NoError(err, "timed out waiting for second incident.escalated webhook (cycle 1)")
}
