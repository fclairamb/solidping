package checkminecraft

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestMinecraftChecker_Type(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	c := &MinecraftChecker{}
	r.Equal(checkerdef.CheckTypeMinecraft, c.Type())
}

func TestMinecraftConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     MinecraftConfig
		wantErr bool
	}{
		{name: "ok java", cfg: MinecraftConfig{Host: "h", Edition: "java"}, wantErr: false},
		{name: "ok bedrock", cfg: MinecraftConfig{Host: "h", Edition: "bedrock"}, wantErr: false},
		{name: "ok empty edition", cfg: MinecraftConfig{Host: "h"}, wantErr: false},
		{name: "missing host", cfg: MinecraftConfig{}, wantErr: true},
		{name: "bad port", cfg: MinecraftConfig{Host: "h", Port: 70000}, wantErr: true},
		{name: "negative port", cfg: MinecraftConfig{Host: "h", Port: -1}, wantErr: true},
		{name: "bad edition", cfg: MinecraftConfig{Host: "h", Edition: "pe"}, wantErr: true},
		{name: "bad timeout too long", cfg: MinecraftConfig{Host: "h", Timeout: 31 * time.Second}, wantErr: true},
		{name: "negative min players", cfg: MinecraftConfig{Host: "h", MinPlayers: -1}, wantErr: true},
		{name: "negative max players", cfg: MinecraftConfig{Host: "h", MaxPlayers: -1}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			err := tt.cfg.Validate()
			if tt.wantErr {
				r.Error(err)
			} else {
				r.NoError(err)
			}
		})
	}
}

func TestMinecraftConfig_FromMap_DefaultsAndCoercion(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &MinecraftConfig{}
	err := cfg.FromMap(map[string]any{
		"host":       "play.example.com",
		"port":       float64(25600),
		"edition":    "bedrock",
		"timeout":    "5s",
		"minPlayers": float64(1),
		"maxPlayers": 100,
	})
	r.NoError(err)
	r.Equal("play.example.com", cfg.Host)
	r.Equal(25600, cfg.Port)
	r.Equal("bedrock", cfg.Edition)
	r.Equal(5*time.Second, cfg.Timeout)
	r.Equal(1, cfg.MinPlayers)
	r.Equal(100, cfg.MaxPlayers)

	// Bad timeout string
	err = (&MinecraftConfig{}).FromMap(map[string]any{"timeout": "not-a-duration"})
	r.Error(err)

	// Wrong-typed host
	err = (&MinecraftConfig{}).FromMap(map[string]any{"host": 1})
	r.Error(err)
}

func TestMinecraftConfig_GetConfig_RoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Defaults are omitted (java edition, default port)
	cfg := &MinecraftConfig{Host: "h", Edition: "java"}
	out := cfg.GetConfig()
	r.Equal("h", out["host"])
	_, hasPort := out["port"]
	r.False(hasPort)
	_, hasEdition := out["edition"]
	r.False(hasEdition)

	// Bedrock edition is preserved
	cfg = &MinecraftConfig{Host: "h", Edition: "bedrock", Port: 19200, Timeout: 3 * time.Second, MinPlayers: 1, MaxPlayers: 5}
	out = cfg.GetConfig()
	r.Equal("h", out["host"])
	r.Equal("bedrock", out["edition"])
	r.Equal(19200, out["port"])
	r.Equal("3s", out["timeout"])
	r.Equal(1, out["minPlayers"])
	r.Equal(5, out["maxPlayers"])
}

func TestMinecraftChecker_Validate_FillsDefaults(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	checker := &MinecraftChecker{}
	spec := &checkerdef.CheckSpec{
		Config: map[string]any{"host": "play.example.com"},
	}
	r.NoError(checker.Validate(spec))
	r.Equal("play.example.com:25565", spec.Name)
	r.Equal("mc-play-example-com", spec.Slug)
}

func TestMinecraftConfig_DefaultPortByEdition(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	java := &MinecraftConfig{Host: "h", Edition: "java"}
	r.Equal(25565, java.resolvePort())

	bedrock := &MinecraftConfig{Host: "h", Edition: "bedrock"}
	r.Equal(19132, bedrock.resolvePort())

	// Explicit port wins over default
	custom := &MinecraftConfig{Host: "h", Edition: "bedrock", Port: 30000}
	r.Equal(30000, custom.resolvePort())
}
