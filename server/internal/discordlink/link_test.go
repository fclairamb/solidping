package discordlink_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/discordlink"
)

// TestMintRefusesImpersonation: a link token binds whichever Discord account
// completes the round trip to the payload's user, so it must never be minted
// under an impersonation token (spec 2026-09-29-03). Positive control: the
// same payload mints, and redeems to the right user, without the impersonator.
func TestMintRefusesImpersonation(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	payload := discordlink.Payload{UserUID: "target-uid", OrgUID: "org-uid"}

	token, err := discordlink.Mint(audit.WithImpersonator(ctx, "admin-uid"), dbSvc, payload)
	r.ErrorIs(err, discordlink.ErrImpersonation)
	r.Empty(token)

	token, err = discordlink.Mint(ctx, dbSvc, payload)
	r.NoError(err)

	got, err := discordlink.Consume(ctx, dbSvc, token)
	r.NoError(err)
	r.NotNil(got)
	r.Equal("target-uid", got.UserUID)
}
