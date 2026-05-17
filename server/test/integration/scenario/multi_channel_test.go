package scenario

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestMultiChannelFanout verifies that a single incident fires on ALL channels
// attached to the escalation step — exercising the fan-out loop in
// job_escalation_step.go.
func TestMultiChannelFanout(t *testing.T) {
	t.Parallel()

	s := NewPostgresScenario(t)
	r := require.New(t)

	// 1. Create two independent webhook collectors (each has its own httptest.Server).
	wc1, srv1 := NewWebhookCollector(t)
	wc2, srv2 := NewWebhookCollector(t)

	// 2. Create a heartbeat check.
	checkUID, hbToken := CreateHeartbeatCheck(t, s, "fanout-check")
	r.NotEmpty(checkUID)

	// 3. Create two webhook channels pointing at the two collectors.
	channelUID1 := CreateWebhookChannel(t, s, srv1.URL)
	channelUID2 := CreateWebhookChannel(t, s, srv2.URL)

	// 4. Create one escalation policy step targeting BOTH channels.
	policyUID := CreateEscalationPolicy(t, s, "fanout-policy", []StepDef{
		{
			DelayMinutes: 0,
			Targets: []TargetDef{
				{Type: "connection", TargetUID: channelUID1},
				{Type: "connection", TargetUID: channelUID2},
			},
		},
	})

	// 5. Assign policy to check.
	AssignPolicyToCheck(t, s, "fanout-check", policyUID)

	// 6. Send a "down" heartbeat.
	SendHeartbeat(t, s, "fanout-check", hbToken, "down")

	// 7. Both collectors must receive incident.escalated.
	//    We wait in parallel so that a slow second notification doesn't push
	//    wc2's wait past the overall deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make([]error, 2)

	wg.Add(2)

	go func() {
		defer wg.Done()
		_, errs[0] = wc1.WaitForWebhook(ctx, func(m map[string]any) bool {
			et, _ := m["eventType"].(string)
			return et == "incident.escalated"
		})
	}()

	go func() {
		defer wg.Done()
		_, errs[1] = wc2.WaitForWebhook(ctx, func(m map[string]any) bool {
			et, _ := m["eventType"].(string)
			return et == "incident.escalated"
		})
	}()

	wg.Wait()

	r.NoError(errs[0], "channel 1 did not receive incident.escalated")
	r.NoError(errs[1], "channel 2 did not receive incident.escalated")
}
