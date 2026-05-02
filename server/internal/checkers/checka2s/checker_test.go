package checka2s

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestA2SChecker_Type(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	c := &A2SChecker{}
	r.Equal(checkerdef.CheckTypeA2S, c.Type())
}

func TestA2SConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     A2SConfig
		wantErr bool
	}{
		{name: "ok", cfg: A2SConfig{Host: "h"}, wantErr: false},
		{name: "missing host", cfg: A2SConfig{}, wantErr: true},
		{name: "bad port", cfg: A2SConfig{Host: "h", Port: 99999}, wantErr: true},
		{name: "bad timeout", cfg: A2SConfig{Host: "h", Timeout: time.Hour}, wantErr: true},
		{name: "negative min players", cfg: A2SConfig{Host: "h", MinPlayers: -1}, wantErr: true},
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

func TestA2SChecker_Validate_FillsDefaults(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	checker := &A2SChecker{}
	spec := &checkerdef.CheckSpec{
		Config: map[string]any{"host": "play.example.com"},
	}
	r.NoError(checker.Validate(spec))
	r.Equal("play.example.com:27015", spec.Name)
	r.Equal("a2s-play-example-com", spec.Slug)
}
