package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	jsconfig "github.com/fclairamb/solidping/server/internal/checkers/checkjs/config"
)

const okScript = `return { status: "up" };`

func TestAIBlockRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	in := map[string]any{
		"script": okScript,
		"ai": map[string]any{
			"prompt":       "log in, the dashboard lists a project",
			"contract":     []any{"login answers 200", "the dashboard shows at least one project"},
			"model":        "glm-5.3-flash",
			"generated_at": "2026-10-03T10:00:00Z",
			"repair":       "auto",
		},
	}

	cfg := &jsconfig.JSConfig{}
	r.NoError(cfg.FromMap(in))
	r.NotNil(cfg.AI)
	r.Equal(jsconfig.RepairAuto, cfg.AI.Repair)
	r.Equal([]string{"login answers 200", "the dashboard shows at least one project"}, cfg.AI.Contract)
	r.Equal(time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC), cfg.AI.GeneratedAt.UTC())
	r.NoError(cfg.Validate())

	out := cfg.GetConfig()
	r.Equal(in["ai"], out["ai"])

	again := &jsconfig.JSConfig{}
	r.NoError(again.FromMap(out))
	r.Equal(cfg.AI, again.AI)
}

func TestAIBlockAbsent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &jsconfig.JSConfig{}
	r.NoError(cfg.FromMap(map[string]any{"script": okScript}))
	r.Nil(cfg.AI)
	r.NotContains(cfg.GetConfig(), "ai")
	r.Equal(jsconfig.RepairOff, cfg.AI.EffectiveRepair())
	r.Equal(jsconfig.RepairPropose, (&jsconfig.AIConfig{}).EffectiveRepair())
}

func TestAIBlockValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		ai   any
		ok   bool
	}{
		{"valid propose", map[string]any{"prompt": "p", "contract": []any{"a"}, "repair": "propose"}, true},
		{"no repair defaults", map[string]any{"prompt": "p", "contract": []any{"a"}}, true},
		{"invalid repair", map[string]any{"prompt": "p", "contract": []any{"a"}, "repair": "always"}, false},
		{"empty contract", map[string]any{"prompt": "p", "contract": []any{}}, false},
		{"missing contract", map[string]any{"prompt": "p"}, false},
		{"blank contract entry", map[string]any{"prompt": "p", "contract": []any{" "}}, false},
		{"contract not strings", map[string]any{"prompt": "p", "contract": []any{1}}, false},
		{"unknown key", map[string]any{"prompt": "p", "contract": []any{"a"}, "provider": "x"}, false},
		{"not an object", "nope", false},
		{"bad timestamp", map[string]any{"prompt": "p", "contract": []any{"a"}, "generated_at": "yesterday"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			spec := &checkerdef.CheckSpec{Config: map[string]any{"script": okScript, "ai": tc.ai}}
			err := jsconfig.ValidateSpec(spec)

			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

// A repair grant round-trips, and NormalizeConfig keeps it only while the
// script and env are the ones it was issued for (spec 2026-10-24-01).
func TestRepairGrantNormalization(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := map[string]any{"BASE_URL": "https://acme.test"}
	grant := jsconfig.RepairDigest(okScript, map[string]string{"BASE_URL": "https://acme.test"})
	in := map[string]any{
		"script": okScript,
		"env":    env,
		"ai": map[string]any{
			"repair": "auto", "repair_hosts": []any{"acme.test"}, "repair_grant": grant,
		},
	}

	cfg := &jsconfig.JSConfig{}
	r.NoError(cfg.FromMap(in))
	r.NoError(cfg.Validate())
	r.Equal(in["ai"], cfg.GetConfig()["ai"])

	hosts, held := cfg.RepairAllowlist()
	r.True(held)
	r.Equal([]string{"acme.test"}, hosts)

	kept, err := cfg.NormalizeConfig(in)
	r.NoError(err)
	r.Equal(in, kept)

	for name, edit := range map[string]func(map[string]any){
		"script edited": func(m map[string]any) { m["script"] = `return { status: "down" };` },
		"env edited":    func(m map[string]any) { m["env"] = map[string]any{"BASE_URL": "https://other.acme.test"} },
	} {
		edited := map[string]any{"script": okScript, "env": env, "ai": in["ai"]}
		edit(edited)

		out, err := cfg.NormalizeConfig(edited)
		r.NoError(err, name)
		r.Equal(map[string]any{"repair": "auto"}, out["ai"], name)
		r.Contains(edited["ai"], "repair_grant", "%s: the input map is not mutated", name)
	}

	// An empty grant still holds, and allows nothing.
	empty := &jsconfig.JSConfig{Script: okScript, AI: &jsconfig.AIConfig{
		RepairHosts: []string{}, RepairGrant: jsconfig.RepairDigest(okScript, nil),
	}}
	emptyBlock, ok := empty.GetConfig()["ai"].(map[string]any)
	r.True(ok)
	r.Equal([]any{}, emptyBlock["repair_hosts"])

	hosts, held = empty.RepairAllowlist()
	r.True(held)
	r.Empty(hosts)
}

// A grant that does not match the stored script and env (a write path that
// skipped NormalizeConfig) fails closed: held, to no host (spec 2026-10-25-01).
// No grant is not held.
func TestRepairAllowlistFailsClosed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	stale := &jsconfig.JSConfig{Script: okScript + "\n// edited", AI: &jsconfig.AIConfig{
		RepairHosts: []string{"acme.test"}, RepairGrant: jsconfig.RepairDigest(okScript, nil),
	}}
	hosts, held := stale.RepairAllowlist()
	r.True(held)
	r.Empty(hosts)

	stored := stale.GetConfig()
	hosts, held = jsconfig.StoredRepairAllowlist(stored)
	r.True(held)
	r.Empty(hosts)

	stored["script"] = okScript
	hosts, held = jsconfig.StoredRepairAllowlist(stored)
	r.True(held, "positive control: the matching grant holds")
	r.Equal([]string{"acme.test"}, hosts)

	// A map that does not parse but carries a grant still fails closed.
	unparsable := map[string]any{"script": 42, "ai": map[string]any{"repair_grant": "abc"}}
	hosts, held = jsconfig.StoredRepairAllowlist(unparsable)
	r.True(held)
	r.Empty(hosts)

	_, held = jsconfig.StoredRepairAllowlist(map[string]any{"script": okScript})
	r.False(held)
	_, held = (&jsconfig.JSConfig{Script: okScript}).RepairAllowlist()
	r.False(held)
}
