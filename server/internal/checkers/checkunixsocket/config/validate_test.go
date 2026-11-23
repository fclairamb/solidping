package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestValidateSpec_Rejects(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		cfg   map[string]any
		param string
	}{
		"empty path":         {map[string]any{"path": ""}, KeyPath},
		"missing path":       {map[string]any{}, KeyPath},
		"relative path":      {map[string]any{"path": "run/app.sock"}, KeyPath},
		"unclean path":       {map[string]any{"path": "/run/../run/app.sock"}, KeyPath},
		"NUL byte":           {map[string]any{"path": "/run/app\x00.sock"}, KeyPath},
		"abstract socket":    {map[string]any{"path": "@app"}, KeyPath},
		"non-string path":    {map[string]any{"path": 12}, KeyPath},
		"http and send_data": {map[string]any{"path": "/run/app.sock", "http_path": "/", "send_data": "x"}, KeyHTTPPath},
		"http and expect":    {map[string]any{"path": "/run/app.sock", "http_path": "/", "expect_data": "x"}, KeyHTTPPath},
		"http path no slash": {map[string]any{"path": "/run/app.sock", "http_path": "health"}, KeyHTTPPath},
		"http path spaces":   {map[string]any{"path": "/run/app.sock", "http_path": "/a b"}, KeyHTTPPath},
		"bad status": {
			map[string]any{"path": "/run/app.sock", "http_path": "/", "expected_status": 42}, KeyExpectedStatus,
		},
		"status without http":  {map[string]any{"path": "/run/app.sock", "expected_status": 200}, KeyExpectedStatus},
		"body without http":    {map[string]any{"path": "/run/app.sock", "body_expect": "ok"}, KeyBodyExpect},
		"timeout too long":     {map[string]any{"path": "/run/app.sock", "timeout": "31s"}, KeyTimeout},
		"timeout not a string": {map[string]any{"path": "/run/app.sock", "timeout": 5}, KeyTimeout},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := ValidateSpec(&checkerdef.CheckSpec{Config: tc.cfg})
			configErr := checkerdef.IsConfigError(err)
			require.NotNil(t, configErr, "want a config error, got %v", err)
			require.Equal(t, tc.param, configErr.Parameter)
		})
	}
}

func TestValidateSpec_Accepts(t *testing.T) {
	t.Parallel()

	cases := map[string]map[string]any{
		"connect only": {"path": "/run/app/app.sock"},
		"raw":          {"path": "/run/app/app.sock", "send_data": "PING\\r\\n", "expect_data": "PONG", "timeout": "2s"},
		"http": {
			"path": "/run/app/app.sock", "http_path": "/healthcheck", "expected_status": 204, "body_expect": "ok",
		},
		"json status": {"path": "/run/app/app.sock", "http_path": "/", "expected_status": float64(200)},
	}

	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			spec := &checkerdef.CheckSpec{Config: cfg}
			require.NoError(t, ValidateSpec(spec))
			require.Equal(t, "unix-socket-app-sock", spec.Slug)
		})
	}
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	in := map[string]any{
		"path": "/run/app/app.sock", "timeout": "2s", "http_path": "/health",
		"expected_status": 200, "body_expect": "ok",
	}

	cfg := &UnixSocketConfig{}
	r.NoError(cfg.FromMap(in))
	r.Equal(in, cfg.GetConfig())

	again := &UnixSocketConfig{}
	r.NoError(again.FromMap(cfg.GetConfig()))
	r.Equal(cfg, again)
}
