package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// TestRDPValidatePreAuthUnchanged pins the pre-auth path's rules exactly as
// they were before credentials existed: the default timeout, the 30 s
// ceiling, and the requirement that credentials travel in PAIRS.
func TestRDPValidatePreAuthUnchanged(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &RDPConfig{Host: "rdp.acme.com"}
	r.NoError(cfg.Validate())
	r.Equal(DefaultPort, cfg.Port)
	r.Equal(DefaultTimeout, cfg.Timeout)
	r.False(cfg.Authenticated())
	r.Equal(time.Duration(0), cfg.MinPeriodHint(), "pre-auth keeps the global floor")
}

func TestRDPValidateTimeoutBounds(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &RDPConfig{}
	r.ErrorContains(cfg.Validate(), "host", "empty config rejected")
	cfg = &RDPConfig{Host: "rdp.acme.com"}
	r.NoError(cfg.Validate())

	cfg = &RDPConfig{Host: "rdp.acme.com", Timeout: 31 * time.Second}
	r.ErrorContains(cfg.Validate(), "must be > 0 and <= 30s", "pre-auth ceiling stays 30s")

	// An authenticated run raises both the default and the ceiling.
	authCfg := &RDPConfig{Host: "rdp.acme.com", Username: "svc-monitor", Password: "secret"}
	r.NoError(authCfg.Validate())
	r.Equal(AuthDefaultTimeout, authCfg.Timeout)
	r.Equal(AuthenticatedMinPeriod, authCfg.MinPeriodHint())

	overCap := &RDPConfig{Host: "rdp.acme.com", Username: "u", Password: "p", Timeout: 91 * time.Second}
	r.ErrorContains(overCap.Validate(), "<= 1m30s", "authenticated ceiling is 90s")

	underCap := &RDPConfig{Host: "rdp.acme.com", Username: "u", Password: "p", Timeout: 31 * time.Second}
	r.NoError(underCap.Validate(), "31s is legal once credentials are set")
}

// TestRDPValidateCredentialPairs pins the pairing rules: username and
// password go together, and screenshot is meaningless without them.
func TestRDPValidateCredentialPairs(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	usernameOnly := &RDPConfig{Host: "rdp.acme.com", Username: "u"}
	r.ErrorContains(usernameOnly.Validate(), "password", "username without password rejected")

	passwordOnly := &RDPConfig{Host: "rdp.acme.com", Password: "p"}
	r.ErrorContains(passwordOnly.Validate(), "username", "password without username rejected")

	shotWithoutCreds := &RDPConfig{Host: "rdp.acme.com", Screenshot: true}
	r.ErrorContains(shotWithoutCreds.Validate(), "requires username and password")
}

// TestRDPValidateEndSession pins the end-session vocabulary: unset means
// logoff, and only logoff/disconnect are accepted.
func TestRDPValidateEndSession(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	defaulted := &RDPConfig{Host: "rdp.acme.com", Username: "u", Password: "p"}
	r.NoError(defaulted.Validate())
	r.Equal(EndSessionLogoff, EndSessionLogoff, "logoff is the documented default")

	for _, mode := range []string{EndSessionLogoff, EndSessionDisconnect} {
		cfg := &RDPConfig{Host: "rdp.acme.com", Username: "u", Password: "p", EndSession: mode}
		r.NoError(cfg.Validate(), mode)
	}

	bad := &RDPConfig{Host: "rdp.acme.com", Username: "u", Password: "p", EndSession: "kill"}
	r.ErrorContains(bad.Validate(), "end_session")
}

// TestRDPFromMapRoundTrip covers the map parsing and serialization of every
// new field, including the secret declaration.
func TestRDPFromMapRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	raw := map[string]any{
		"host":        "rdp.acme.com",
		"port":        3390,
		"timeout":     "40s",
		"username":    "svc-monitor",
		"password":    "hunter2",
		"domain":      "acme",
		"screenshot":  true,
		"end_session": "disconnect",
	}

	cfg := &RDPConfig{}
	r.NoError(cfg.FromMap(raw))
	r.NoError(cfg.Validate())
	r.Equal("svc-monitor", cfg.Username)
	r.Equal("hunter2", cfg.Password)
	r.Equal("acme", cfg.Domain)
	r.True(cfg.Screenshot)
	r.Equal(EndSessionDisconnect, cfg.EndSession)
	r.True(cfg.Authenticated())

	back := cfg.GetConfig()
	r.Equal("svc-monitor", back["username"])
	r.Equal("hunter2", back["password"])
	r.Equal("acme", back["domain"])
	r.Equal(true, back["screenshot"])
	r.Equal("disconnect", back["end_session"])
	r.Equal("40s", back["timeout"])

	// end_session=logoff is the default and is not serialized: the public
	// config stays small and the meaning is unchanged.
	logoffCfg := &RDPConfig{Host: "rdp.acme.com", Username: "u", Password: "p", EndSession: EndSessionLogoff}
	r.NoError(logoffCfg.Validate())
	_, present := logoffCfg.GetConfig()["end_session"]
	r.False(present, "default end_session is not serialized")
}

// TestRDPTypeErrors pins the type errors for every new field.
func TestRDPTypeErrors(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for field, bad := range map[string]any{
		"username":    42,
		"password":    []string{"x"},
		"domain":      3.14,
		"screenshot":  "yes",
		"end_session": 7,
	} {
		cfg := &RDPConfig{Host: "rdp.acme.com"}
		err := cfg.FromMap(map[string]any{"host": "rdp.acme.com", field: bad})
		r.ErrorContains(err, field, "type error names the field")
	}
}

// TestRDPSecretFields declares `password` as the one secret key, so the
// credential splitter moves it into the encrypted envelope.
func TestRDPSecretFields(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &RDPConfig{Username: "u", Password: "p"}
	r.Equal([]string{"password"}, cfg.SecretFields())
}

// TestRDPMinPeriodHintBothPaths pins the floor decision: credentials raise
// the floor to 15 minutes, the pre-auth check keeps the global floor (no
// opinion).
func TestRDPMinPeriodHintBothPaths(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	preAuth := &RDPConfig{Host: "rdp.acme.com"}
	r.Equal(time.Duration(0), preAuth.MinPeriodHint())

	halfOnly := &RDPConfig{Host: "rdp.acme.com", Username: "u"}
	r.Equal(time.Duration(0), halfOnly.MinPeriodHint(), "half a credential pair is not an authenticated run")

	authed := &RDPConfig{Host: "rdp.acme.com", Username: "u", Password: "p"}
	r.Equal(15*time.Minute, authed.MinPeriodHint())
}

// TestValidateSpecWithCredentials runs the spec-level validator (the entry
// `sp checks validate` uses) over an authenticated config.
func TestValidateSpecWithCredentials(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	spec := &checkerdef.CheckSpec{
		Config: map[string]any{
			"host":     "rdp.acme.com",
			"username": "svc-monitor",
			"password": "hunter2",
		},
	}
	r.NoError(ValidateSpec(spec))
	r.Equal("RDP: rdp.acme.com", spec.Name)
	r.Equal("rdp-rdp-acme-com", spec.Slug)
}
