package agents_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/handlers/agents"
	"github.com/fclairamb/solidping/server/internal/sealing"
)

// TestRegionKeyLifecycle (spec 2026-11-08-01 §2): a private region starts per
// agent, takes a shared region key (public half only), rotates it and goes
// back; a foreign region and a malformed recipient are refused.
func TestRegionKeyLifecycle(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newSetup(t)
	ctx := t.Context()

	_, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "dc1", Name: "DC1"})
	r.NoError(err)

	list, err := s.svc.ListPrivateRegions(ctx, "acme")
	r.NoError(err)
	r.Equal(sealing.KeyModePerAgent, list.Data[0].KeyMode)
	r.Nil(list.Data[0].RegionKey)

	_, recipient, err := credentials.GenerateX25519Keypair()
	r.NoError(err)

	key, err := s.svc.SetRegionKey(ctx, "acme", "dc1", &agents.SetRegionKeyRequest{Recipient: recipient})
	r.NoError(err)
	r.Equal(credentials.RecipientFingerprint(recipient), key.Fingerprint)

	list, err = s.svc.ListPrivateRegions(ctx, "acme")
	r.NoError(err)
	r.Equal(sealing.KeyModeShared, list.Data[0].KeyMode)
	r.Equal(recipient, list.Data[0].RegionKey.Recipient)
	r.Zero(list.Data[0].AgentsWithRegionKey, "no agent advertises the key yet")

	stored, err := sealing.RegionKeys(ctx, s.dbSvc, s.org.UID)
	r.NoError(err)
	r.Equal(recipient, stored["@dc1"].Recipient)

	// Rotation replaces the current key.
	_, rotated, err := credentials.GenerateX25519Keypair()
	r.NoError(err)
	_, err = s.svc.SetRegionKey(ctx, "acme", "dc1", &agents.SetRegionKeyRequest{Recipient: rotated})
	r.NoError(err)

	stored, err = sealing.RegionKeys(ctx, s.dbSvc, s.org.UID)
	r.NoError(err)
	r.Equal(rotated, stored["@dc1"].Recipient)

	_, err = s.svc.SetRegionKey(ctx, "acme", "dc1", &agents.SetRegionKeyRequest{Recipient: "AGE-SECRET-KEY-1NOPE"})
	r.ErrorIs(err, agents.ErrInvalidRegionKey)

	_, err = s.svc.SetRegionKey(ctx, "acme", "nowhere", &agents.SetRegionKeyRequest{Recipient: rotated})
	r.ErrorIs(err, agents.ErrRegionNotFound)

	r.NoError(s.svc.DeleteRegionKey(ctx, "acme", "dc1"))

	list, err = s.svc.ListPrivateRegions(ctx, "acme")
	r.NoError(err)
	r.Equal(sealing.KeyModePerAgent, list.Data[0].KeyMode)
}
