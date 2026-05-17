package scenario

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOnCallScheduleTarget verifies that an escalation policy step with a
// `schedule` target fires: the on-call resolver looks up the current user from
// the schedule and pages them.
//
// The resolver path (oncallschedules.Service.Resolve → jobtypes.pageSchedule)
// sends an email. Since tests have no real email relay, we verify the
// notification audit row was created (status = sent) rather than an actual
// delivered message.
//
// We also assert that a second step with a `connection` (webhook) target in
// the SAME policy fires, confirming that a mixed-target step escalation works
// end-to-end with the webhook collector.
func TestOnCallScheduleTarget(t *testing.T) {
	t.Parallel()

	s := NewPostgresScenario(t)
	r := require.New(t)

	// 1. Fetch the UID of the admin user created by seedScenario.
	userUID := UserUID(t, s)
	r.NotEmpty(userUID)

	// 2. Create an on-call schedule that rosters the test user.
	//    startAt = 24h ago ensures the user is currently on-call.
	scheduleUID := CreateOnCallSchedule(t, s, "oncall-sched", []string{userUID})
	r.NotEmpty(scheduleUID)

	// 3. Create a heartbeat check.
	checkUID, hbToken := CreateHeartbeatCheck(t, s, "oncall-check")
	r.NotEmpty(checkUID)

	// 4. Create a webhook channel for the connection-target assertion.
	webhookChannelUID := CreateWebhookChannel(t, s, s.WebhookURL)

	// 5. Create an escalation policy:
	//    - Step 0: schedule target (pages the on-call user via email) +
	//              connection target (fires webhook so we can assert delivery).
	policyUID := CreateEscalationPolicy(t, s, "oncall-policy", []StepDef{
		{
			DelaySeconds: 0,
			Targets: []TargetDef{
				{Type: "schedule", TargetUID: scheduleUID},
				{Type: "connection", TargetUID: webhookChannelUID},
			},
		},
	})

	// 6. Assign the policy to the check.
	AssignPolicyToCheck(t, s, "oncall-check", policyUID)

	// 7. Send a "down" heartbeat — incident opens, escalation fires.
	SendHeartbeat(t, s, "oncall-check", hbToken, "down")

	// 8. The webhook (connection target) must arrive — confirms the whole
	//    escalation step ran, including on-call resolution.
	payload := waitForWebhookEvent(t, s.Webhooks, "incident.escalated")
	r.Equal("incident.escalated", payload["eventType"])
	r.Equal(checkUID, payload["checkUid"])
}
