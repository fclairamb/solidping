package checkheartbeat_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkheartbeat"
)

// TestRequireHMACFromConfig covers the security-relevant half of the flag: a
// mistyped value is an error, never a silent downgrade to permissive.
func TestRequireHMACFromConfig(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Positive controls.
	got, err := checkheartbeat.RequireHMACFromConfig(map[string]any{"require_hmac": true})
	r.NoError(err)
	r.True(got)

	got, err = checkheartbeat.RequireHMACFromConfig(map[string]any{"require_hmac": false})
	r.NoError(err)
	r.False(got)

	// Absent / explicit null default to false, matching today's posture.
	for _, cfg := range []map[string]any{{}, {"require_hmac": nil}, {"token": "abc"}} {
		got, err = checkheartbeat.RequireHMACFromConfig(cfg)
		r.NoError(err)
		r.False(got)
	}

	// A mistyped value must NOT read as false — that would be a check that
	// silently kept accepting plaintext-token beats after an operator believed
	// they had turned them off.
	for _, bad := range []any{"true", "yes", 1, 0, []any{true}, map[string]any{}} {
		_, err = checkheartbeat.RequireHMACFromConfig(map[string]any{"require_hmac": bad})
		r.Error(err)
		r.NotNil(checkerdef.IsConfigError(err))
	}
}

// TestValidateRejectsMistypedRequireHMAC proves the rejection reaches the
// create/update path, not just the helper.
func TestValidateRejectsMistypedRequireHMAC(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &checkheartbeat.HeartbeatChecker{}

	spec := &checkerdef.CheckSpec{Config: map[string]any{"require_hmac": "true"}}
	r.Error(checker.Validate(spec))

	// Positive control: the boolean form validates and a token is minted.
	ok := &checkerdef.CheckSpec{Config: map[string]any{"require_hmac": true}}
	r.NoError(checker.Validate(ok))
	r.NotEmpty(ok.Config["token"])
	r.Equal(true, ok.Config["require_hmac"])
}

// TestConfigRoundTrip pins the map<->struct mapping the registry relies on.
func TestConfigRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &checkheartbeat.HeartbeatConfig{}
	r.NoError(cfg.FromMap(map[string]any{"token": "tok", "require_hmac": true}))
	r.Equal("tok", cfg.Token)
	r.True(cfg.RequireHMAC)
	r.Equal(map[string]any{"token": "tok", "require_hmac": true}, cfg.GetConfig())

	// The flag is omitted when false, so a default check's config stays as
	// small as it is today.
	off := &checkheartbeat.HeartbeatConfig{Token: "tok"}
	r.Equal(map[string]any{"token": "tok"}, off.GetConfig())
}
