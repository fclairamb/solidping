package config

import (
	"strings"
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

// TestVNCUsernameAndTLSKeys: username, tlsVerify and the certificate
// thresholds survive a FromMap/GetConfig round trip; tlsVerify defaults to
// false and is only written when set.
func TestVNCUsernameAndTLSKeys(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &VNCConfig{}
	r.NoError(cfg.FromMap(map[string]any{
		"host": "h", "password": "pw", "username": "alice", "tlsVerify": true,
		"warningDays": float64(30), "criticalDays": float64(7),
	}))
	r.Equal("alice", cfg.Username)
	r.True(cfg.TLSVerify)
	r.Equal(30, cfg.WarningDays)
	r.Equal(7, cfg.CriticalDays)
	r.NoError(cfg.Validate())

	out := cfg.GetConfig()
	r.Equal("alice", out["username"])
	r.Equal(true, out["tlsVerify"])
	r.Equal(30, out["warningDays"])
	r.Equal(7, out["criticalDays"])

	def := &VNCConfig{}
	r.NoError(def.FromMap(map[string]any{"host": "h"}))
	r.False(def.TLSVerify, "tlsVerify defaults to false")
	r.NotContains(def.GetConfig(), "tlsVerify")

	r.ErrorContains((&VNCConfig{}).FromMap(map[string]any{"username": 1}), "username")
	r.ErrorContains((&VNCConfig{}).FromMap(map[string]any{"tlsVerify": "yes"}), "tlsVerify")
	r.ErrorContains((&VNCConfig{}).FromMap(map[string]any{"warningDays": "x"}), "warningDays")
}

// TestVNCUsernameValidation: a username needs a password and fits Apple
// Remote Desktop's 64-byte field (63 bytes + terminator). 63 bytes passes
// (positive control), 64 is rejected.
func TestVNCUsernameValidation(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.ErrorContains((&VNCConfig{Host: "h", Username: "alice"}).Validate(), "username")
	r.NoError((&VNCConfig{Host: "h", Username: "alice", Password: "pw"}).Validate())

	ok := strings.Repeat("u", MaxUsernameLen)
	r.NoError((&VNCConfig{Host: "h", Username: ok, Password: "pw"}).Validate())

	err := (&VNCConfig{Host: "h", Username: ok + "u", Password: "pw"}).Validate()
	r.ErrorContains(err, "username")
	r.ErrorContains(err, "63")
}

// TestVNCThresholdValidation: negative, oversized and inverted thresholds
// are refused.
func TestVNCThresholdValidation(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.ErrorContains((&VNCConfig{Host: "h", WarningDays: -1}).Validate(), "warningDays")
	r.ErrorContains((&VNCConfig{Host: "h", CriticalDays: 4000}).Validate(), "criticalDays")
	r.ErrorContains((&VNCConfig{Host: "h", WarningDays: 5, CriticalDays: 10}).Validate(), "warningDays")
	r.NoError((&VNCConfig{Host: "h", WarningDays: 30, CriticalDays: 7}).Validate())
}
