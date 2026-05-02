package checkemail_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkemail"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestEmailCheckerType(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	c := &checkemail.EmailChecker{}
	r.Equal(checkerdef.CheckTypeEmail, c.Type())
}

func TestValidateGeneratesTokenWhenMissing(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	c := &checkemail.EmailChecker{}
	spec := &checkerdef.CheckSpec{}
	r.NoError(c.Validate(spec))

	token, ok := spec.Config["token"].(string)
	r.True(ok)
	r.Len(token, 48, "expected 48-character hex token")
}

func TestValidatePreservesExistingToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	c := &checkemail.EmailChecker{}
	original := "feedfacefeedfacefeedfacefeedfacefeedfacefeedface"
	spec := &checkerdef.CheckSpec{
		Config: map[string]any{"token": original},
	}
	r.NoError(c.Validate(spec))
	r.Equal(original, spec.Config["token"])
}

func TestValidateRejectsNonStringToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	c := &checkemail.EmailChecker{}
	spec := &checkerdef.CheckSpec{
		Config: map[string]any{"token": 12345},
	}
	// Validate auto-generates because the type assertion fails — same
	// behavior as heartbeat. The spec doesn't surface a config error
	// here; callers passing a bad token shape just get one regenerated.
	r.NoError(c.Validate(spec))

	token, ok := spec.Config["token"].(string)
	r.True(ok)
	r.Len(token, 48)
}

func TestValidateFillsNameAndSlug(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	c := &checkemail.EmailChecker{}
	spec := &checkerdef.CheckSpec{}
	r.NoError(c.Validate(spec))
	r.Equal("email", spec.Name)
	r.Equal("email", spec.Slug)
}

func TestExecuteReturnsErrNotExecutable(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	c := &checkemail.EmailChecker{}
	cfg := &checkemail.EmailConfig{Token: "abcd"}
	_, err := c.Execute(context.Background(), cfg)
	r.ErrorIs(err, checkemail.ErrNotExecutable)
}

func TestEmailConfigFromMapAndGetConfigRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &checkemail.EmailConfig{}
	r.NoError(cfg.FromMap(map[string]any{"token": "abc"}))
	r.Equal("abc", cfg.Token)

	roundTripped := cfg.GetConfig()
	r.Equal(map[string]any{"token": "abc"}, roundTripped)
}

func TestEmailConfigFromMapRejectsNonString(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &checkemail.EmailConfig{}
	err := cfg.FromMap(map[string]any{"token": 42})
	r.Error(err)

	var configErr *checkerdef.ConfigError
	r.ErrorAs(err, &configErr)
	r.Equal("token", configErr.Parameter)
}
