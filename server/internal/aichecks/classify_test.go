package aichecks_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/aichecks"
	jsconfig "github.com/fclairamb/solidping/server/internal/checkers/checkjs/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestClassifyFailure(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status models.ResultStatus
		output map[string]any
		want   aichecks.FailureClass
	}{
		{"up", models.ResultStatusUp, nil, aichecks.FailureNone},
		{"exception", models.ResultStatusError,
			map[string]any{"error": "script error: TypeError: Cannot read property 'x' of undefined"}, aichecks.FailureDrift},
		{"missing return", models.ResultStatusError,
			map[string]any{"error": "script must return a result object"}, aichecks.FailureDrift},
		{"drift tag on down", models.ResultStatusDown, map[string]any{"failure": "drift"}, aichecks.FailureDrift},
		{"assertion tag", models.ResultStatusDown, map[string]any{"failure": "assertion"}, aichecks.FailureAssertion},
		{"untagged down", models.ResultStatusDown, map[string]any{"reason": "503"}, aichecks.FailureAssertion},
		{"timeout", models.ResultStatusTimeout, map[string]any{"error": "script timed out after 30s"}, aichecks.FailureTimeout},
		{"connection refused", models.ResultStatusError,
			map[string]any{"error": "script error: GoError: dial tcp 10.0.0.1:443: connect: connection refused"},
			aichecks.FailureConnection},
		{"no such host", models.ResultStatusError,
			map[string]any{"error": "script error: lookup acme.invalid: no such host"}, aichecks.FailureConnection},
		{"error tagged assertion", models.ResultStatusError,
			map[string]any{"failure": "assertion", "error": "boom"}, aichecks.FailureAssertion},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, aichecks.ClassifyFailure(tc.status, tc.output))
		})
	}
}

// TestShouldRepair walks the repair trigger: only drift rows with every gate
// open start a repair.
func TestShouldRepair(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	open := aichecks.Gates{
		Mode: jsconfig.RepairPropose, Class: aichecks.FailureDrift, ConsecutiveFailures: 3,
		TargetHealthy: true, Now: now,
	}

	with := func(edit func(g *aichecks.Gates)) aichecks.Gates {
		g := open
		edit(&g)

		return g
	}

	cases := []struct {
		name   string
		gates  aichecks.Gates
		want   bool
		reason string
	}{
		{"drift, all gates open", open, true, ""},
		{"drift, auto mode", with(func(g *aichecks.Gates) { g.Mode = jsconfig.RepairAuto }), true, ""},
		{"repair off", with(func(g *aichecks.Gates) { g.Mode = jsconfig.RepairOff }), false, aichecks.ReasonRepairOff},
		{"assertion", with(func(g *aichecks.Gates) { g.Class = aichecks.FailureAssertion }), false, aichecks.ReasonNotDrift},
		{"timeout", with(func(g *aichecks.Gates) { g.Class = aichecks.FailureTimeout }), false, aichecks.ReasonNotDrift},
		{"connection refused", with(func(g *aichecks.Gates) { g.Class = aichecks.FailureConnection }),
			false, aichecks.ReasonNotDrift},
		{"two failures only", with(func(g *aichecks.Gates) { g.ConsecutiveFailures = 2 }), false, aichecks.ReasonNotEnough},
		{"custom threshold", with(func(g *aichecks.Gates) { g.ConsecutiveFailures = 2; g.Threshold = 2 }), true, ""},
		{"target unhealthy", with(func(g *aichecks.Gates) { g.TargetHealthy = false }), false, aichecks.ReasonTargetDown},
		{"attempted 2 h ago", with(func(g *aichecks.Gates) { g.LastAttempt = now.Add(-2 * time.Hour) }),
			false, aichecks.ReasonCheckCooldown},
		{"attempted 25 h ago", with(func(g *aichecks.Gates) { g.LastAttempt = now.Add(-25 * time.Hour) }), true, ""},
		{"org cap reached", with(func(g *aichecks.Gates) { g.OrgAttemptsToday = aichecks.DefaultOrgDailyAttempts }),
			false, aichecks.ReasonOrgCap},
		{"org under custom cap", with(func(g *aichecks.Gates) { g.OrgAttemptsToday = 1; g.OrgDailyCap = 2 }), true, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ok, reason := aichecks.ShouldRepair(tc.gates)
			require.Equal(t, tc.want, ok)
			require.Equal(t, tc.reason, reason)
		})
	}
}

func TestWantsRepair(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.False(aichecks.WantsRepair(map[string]any{"script": "x"}))
	r.True(aichecks.WantsRepair(map[string]any{"ai": map[string]any{"prompt": "p"}}))
	r.True(aichecks.WantsRepair(map[string]any{"ai": map[string]any{"repair": "auto"}}))
	r.False(aichecks.WantsRepair(map[string]any{"ai": map[string]any{"repair": "off"}}))
}
