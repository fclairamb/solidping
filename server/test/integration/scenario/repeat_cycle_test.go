package scenario

import "testing"

// TestRepeatCycle verifies that a repeat-enabled escalation policy fires
// multiple paging cycles when an incident remains open.
//
// This test requires sub-minute escalation step delays, which are blocked on
// spec 2026-05-17-05-sub-minute-escalation-step-delays.md. It is skipped
// until that spec ships.
func TestRepeatCycle(t *testing.T) {
	t.Parallel()

	t.Skip("requires sub-minute step delays: 2026-05-17-05")
}
