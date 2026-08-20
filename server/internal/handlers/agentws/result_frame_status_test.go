package agentws

import (
	"testing"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestValidateResultFrameRejectsServerMintedStatuses pins the untrusted-input
// boundary for spec 2026-08-18-10: ResultStatusAbandoned is minted by the
// server's reaper and by nothing else, so a deported agent must never be able
// to claim it — an agent that could self-report "abandoned" could quietly
// erase its own failures from every availability percentage.
//
// The accepted-range assertions are the positive control: without them a
// validator that rejected every frame would pass the rejection half alone.
func TestValidateResultFrameRejectsServerMintedStatuses(t *testing.T) {
	t.Parallel()

	frame := func(status int) *agentcrypto.ClientFrame {
		return &agentcrypto.ClientFrame{JobUID: "job-1", Status: status}
	}

	t.Run("abandoned is rejected", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		r.Equal("status out of range", validateResultFrame(frame(int(models.ResultStatusAbandoned))),
			"an agent must never be able to report the reaper's own status")
	})

	t.Run("statuses an agent may legitimately report are accepted", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		for _, status := range []models.ResultStatus{
			models.ResultStatusCreated,
			models.ResultStatusRunning,
			models.ResultStatusUp,
			models.ResultStatusDown,
			models.ResultStatusTimeout,
			models.ResultStatusError,
		} {
			r.Empty(validateResultFrame(frame(int(status))), "status %d must be accepted", status)
		}
	})
}
