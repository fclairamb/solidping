package integrations

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestValidateEmailSettings(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.NoError(validateConnectionType(models.ConnectionTypeEmail, models.JSONMap{"to": []any{"a@acme.com"}}))
	r.NoError(validateConnectionType(models.ConnectionTypeEmail, models.JSONMap{}))
	r.ErrorIs(validateConnectionType(models.ConnectionTypeEmail,
		models.JSONMap{"to": []any{"a@acme.com", "not-an-address"}}), ErrInvalidSettings)
	r.ErrorIs(validateConnectionType(models.ConnectionTypeEmail,
		models.JSONMap{"to": []any{42}}), ErrInvalidSettings)
}
