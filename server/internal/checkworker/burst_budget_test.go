package checkworker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkicmp"
)

// TestResolveBurstBudget pins the spec 2026-09-21-01 seam: a user-set
// per-packet `timeout` must no longer cap the whole burst — the execution
// budget grows to the config's own worst-case burst cost — while non-burst
// configs and already-sufficient budgets pass through untouched.
func TestResolveBurstBudget(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	tests := []struct {
		name         string
		config       checkerdef.Config
		checkTimeout time.Duration
		want         time.Duration
	}{
		{
			name:         "user-set timeout no longer caps the burst",
			config:       &checkicmp.ICMPConfig{Count: 10, Timeout: time.Second, Interval: 100 * time.Millisecond},
			checkTimeout: time.Second,
			want:         10*time.Second + 900*time.Millisecond,
		},
		{
			name:         "unset timeout falls back to the checker default (5s per packet)",
			config:       &checkicmp.ICMPConfig{Count: 3, Interval: 50 * time.Millisecond},
			checkTimeout: 15 * time.Second,
			want:         15*time.Second + 100*time.Millisecond,
		},
		{
			name:         "count 1 keeps the resolved budget (defaults: 5s burst, budget 15s)",
			config:       &checkicmp.ICMPConfig{},
			checkTimeout: 15 * time.Second,
			want:         15 * time.Second,
		},
		{
			name:         "non-burst config passes through unchanged",
			config:       plainConfig{},
			checkTimeout: 7 * time.Second,
			want:         7 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r.Equal(tt.want, resolveBurstBudget(tt.config, tt.checkTimeout))
		})
	}
}

// plainConfig is a minimal Config that deliberately does not implement
// checkerdef.BurstBudgeter.
type plainConfig struct{}

func (plainConfig) FromMap(map[string]any) error { return nil }

func (plainConfig) GetConfig() map[string]any { return nil }
