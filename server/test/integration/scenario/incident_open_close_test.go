package scenario

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIncidentOpenAndClose exercises the full check-down → escalation webhook
// → check-up → incident.resolved webhook pipeline.
//
// Pipeline path exercised:
//
//	heartbeat POST
//	  → incidentSvc.ProcessCheckResult (sync, in-handler)
//	  → scheduleEscalationPolicy
//	  → escalation step job (job worker picks up via NOTIFY/wakeup)
//	  → notification job  (event: incident.escalated)
//	  → WebhookSender.Send → in-test httptest.Server
//
//	recovery heartbeat POST
//	  → incidentSvc.ProcessCheckResult
//	  → resolveIncident → queueNotifications  (event: incident.resolved)
//	  → notification job
//	  → WebhookSender.Send → in-test httptest.Server
func TestIncidentOpenAndClose(t *testing.T) {
	t.Parallel()

	s := NewPostgresScenario(t)
	r := require.New(t)

	// 1. Create a heartbeat check with immediate confirmation and recovery.
	checkUID, hbToken := CreateHeartbeatCheck(t, s, "open-close-check")
	r.NotEmpty(checkUID)
	r.NotEmpty(hbToken)

	// 2. Create a webhook channel pointing at the in-test receiver.
	channelUID := CreateWebhookChannel(t, s, s.WebhookURL)

	// 3. Bind the webhook channel to the check so it receives incident.resolved.
	//    The check-channel binding fires for created/resolved via queueNotifications.
	BindChannelToCheck(t, s, "open-close-check", channelUID)

	// 4. Create an escalation policy with one step (delay=0) targeting the webhook.
	//    The escalation policy fires incident.escalated when the incident opens.
	policyUID := CreateEscalationPolicy(t, s, "open-close-policy", []StepDef{
		{
			DelayMinutes: 0,
			Targets: []TargetDef{
				{Type: "connection", TargetUID: channelUID},
			},
		},
	})

	// 5. Assign the policy to the check.
	AssignPolicyToCheck(t, s, "open-close-check", policyUID)

	// 6. Send a "down" heartbeat — incident opens, escalation fires.
	SendHeartbeat(t, s, "open-close-check", hbToken, "down")

	// 7. Wait for incident.escalated (sent by the escalation step job).
	escalated := waitForWebhookEvent(t, s.Webhooks, "incident.escalated")
	r.Equal("incident.escalated", escalated["eventType"])
	r.Equal(checkUID, escalated["checkUid"], "checkUid must match")
	r.NotEmpty(escalated["incidentUid"], "incidentUid must be non-empty")
	r.Equal("open-close-check", escalated["checkName"])
	r.NotNil(escalated["startedAt"])
	r.Nil(escalated["resolvedAt"], "resolvedAt must be absent on escalation")

	// 8. Send an "up" heartbeat — incident resolves.
	SendHeartbeat(t, s, "open-close-check", hbToken, "up")

	// 9. Wait for incident.resolved (sent via direct check-channel binding).
	resolved := waitForWebhookEvent(t, s.Webhooks, "incident.resolved")
	r.Equal("incident.resolved", resolved["eventType"])
	r.Equal(checkUID, resolved["checkUid"])
	r.NotNil(resolved["resolvedAt"], "resolvedAt must be set on incident.resolved")
}
