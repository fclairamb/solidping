package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

func TestVNCValidateRequiresHost(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.ErrorContains((&VNCConfig{}).Validate(), "host")

	cfg := &VNCConfig{Host: "vnc.acme.com"}
	r.NoError(cfg.Validate())
	r.Equal(DefaultPort, cfg.Port)
	r.Equal(DefaultTimeout, cfg.Timeout)
	r.True(cfg.RequiresAuth(), "requireAuth defaults to true")
	r.False(cfg.Authenticated())
}

func TestVNCValidatePortRange(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.ErrorContains((&VNCConfig{Host: "h", Port: -1}).Validate(), "port")
	r.ErrorContains((&VNCConfig{Host: "h", Port: 65536}).Validate(), "port")
	r.NoError((&VNCConfig{Host: "h", Port: 1}).Validate())
	r.NoError((&VNCConfig{Host: "h", Port: 65535}).Validate())
}

func TestVNCValidateTimeout(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.ErrorContains((&VNCConfig{Host: "h", Timeout: 61 * time.Second}).Validate(), "timeout")
	r.ErrorContains((&VNCConfig{Host: "h", Timeout: -time.Second}).Validate(), "timeout")
	r.NoError((&VNCConfig{Host: "h", Timeout: 60 * time.Second}).Validate())
}

// TestVNCScreenshotNeedsPassword: a screenshot is only possible after
// authentication, so the combination without a password is refused, and the
// same config with a password is accepted (positive control).
func TestVNCScreenshotNeedsPassword(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.ErrorContains((&VNCConfig{Host: "h", Screenshot: true}).Validate(), "screenshot")
	r.NoError((&VNCConfig{Host: "h", Screenshot: true, Password: "secret"}).Validate())
}

// TestVNCSecretFields declares `password` as the secret key, so the credential
// splitter moves it out of the public config.
func TestVNCSecretFields(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &VNCConfig{Host: "vnc.acme.com", Password: "hunter2", Screenshot: true}
	r.Equal([]string{"password"}, credentials.SecretFieldsFor(cfg))

	public, private := credentials.SplitConfig(cfg.GetConfig(), credentials.SecretFieldsFor(cfg))
	r.NotContains(public, "password")
	r.Equal("hunter2", private["password"])
	r.Equal("vnc.acme.com", public["host"])
}

func TestVNCFromMapRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	in := map[string]any{
		"host":        "vnc.acme.com",
		"port":        float64(5901),
		"timeout":     "20s",
		"password":    "secret",
		"requireAuth": false,
		"screenshot":  true,
	}

	cfg := &VNCConfig{}
	r.NoError(cfg.FromMap(in))
	r.Equal(5901, cfg.Port)
	r.Equal(20*time.Second, cfg.Timeout)
	r.False(cfg.RequiresAuth())
	r.True(cfg.Screenshot)

	out := cfg.GetConfig()
	r.Equal(false, out["requireAuth"])
	r.Equal("20s", out["timeout"])
	r.Equal(5901, out["port"])

	// The default (true) is not written back.
	def := &VNCConfig{Host: "h"}
	r.NotContains(def.GetConfig(), "requireAuth")
}

func TestVNCFromMapTypeErrors(t *testing.T) {
	t.Parallel()

	for key, bad := range map[string]any{
		"host": 1, "port": "x", "timeout": 3, "password": 1, "requireAuth": "yes", "screenshot": "no",
	} {
		cfg := &VNCConfig{}
		require.Error(t, cfg.FromMap(map[string]any{key: bad}), key)
	}

	r := require.New(t)
	r.Error((&VNCConfig{}).FromMap(map[string]any{"timeout": "nope"}))
}

func TestVNCValidateSpecDefaults(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	spec := &checkerdef.CheckSpec{Config: map[string]any{"host": "vnc.acme.com"}}
	r.NoError(ValidateSpec(spec))
	r.Equal("VNC: vnc.acme.com", spec.Name)
	r.Equal("vnc-vnc-acme-com", spec.Slug)

	bad := &checkerdef.CheckSpec{Config: map[string]any{"host": "h", "screenshot": true}}
	r.Error(ValidateSpec(bad))
}
