package oauthstate_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/oauthstate"
)

func setupDB(t *testing.T) (context.Context, *sqlite.Service) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))

	t.Cleanup(func() { _ = dbService.Close() })

	return ctx, dbService
}

func TestGenerateValidate_RoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, db := setupDB(t)

	payload := map[string]any{"redirectUri": "/orgs/example"}
	nonce, err := oauthstate.Generate(ctx, db, "slack-signin", payload, time.Minute)
	r.NoError(err)
	r.NotEmpty(nonce)

	entry, err := oauthstate.Validate(ctx, db, "slack-signin", nonce)
	r.NoError(err)
	r.Equal(nonce, entry.Nonce)
	r.Equal("slack-signin", entry.Kind)
	r.Equal("/orgs/example", entry.Payload["redirectUri"])
}

func TestValidate_RejectsReuse(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, db := setupDB(t)

	nonce, err := oauthstate.Generate(ctx, db, "slack-install", nil, time.Minute)
	r.NoError(err)

	_, err = oauthstate.Validate(ctx, db, "slack-install", nonce)
	r.NoError(err)

	_, err = oauthstate.Validate(ctx, db, "slack-install", nonce)
	r.ErrorIs(err, oauthstate.ErrInvalidState)
}

func TestValidate_RejectsWrongKind(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, db := setupDB(t)

	nonce, err := oauthstate.Generate(ctx, db, "slack-signin", nil, time.Minute)
	r.NoError(err)

	_, err = oauthstate.Validate(ctx, db, "slack-install", nonce)
	r.ErrorIs(err, oauthstate.ErrInvalidState)
}

func TestValidate_RejectsUnknown(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, db := setupDB(t)

	_, err := oauthstate.Validate(ctx, db, "slack-install", "definitely-not-a-real-nonce")
	r.ErrorIs(err, oauthstate.ErrInvalidState)
}

func TestValidate_RejectsEmpty(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, db := setupDB(t)

	_, err := oauthstate.Validate(ctx, db, "", "x")
	r.ErrorIs(err, oauthstate.ErrInvalidState)

	_, err = oauthstate.Validate(ctx, db, "slack-install", "")
	r.ErrorIs(err, oauthstate.ErrInvalidState)
}

func TestGenerate_KindRequired(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, db := setupDB(t)

	_, err := oauthstate.Generate(ctx, db, "", nil, time.Minute)
	r.Error(err)
}
