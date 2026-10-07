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
