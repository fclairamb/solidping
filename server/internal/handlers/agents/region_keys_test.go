package agents_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
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

// TestDeletedRegionKeyDoesNotReachANewRegion (spec 2026-11-11-03): deleting a
// region takes its shared key with it, so a region created later with the
// same slug is per agent, and the old key is retired like a removed one.
func TestDeletedRegionKeyDoesNotReachANewRegion(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newSetup(t)
	ctx := t.Context()

	_, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "office"})
	r.NoError(err)

	_, recipient, err := credentials.GenerateX25519Keypair()
	r.NoError(err)
	_, err = s.svc.SetRegionKey(ctx, "acme", "office", &agents.SetRegionKeyRequest{Recipient: recipient})
	r.NoError(err)

	// Positive control: while the region exists, the key is its recipient.
	got, err := sealing.RecipientsForRegions(ctx, s.dbSvc, s.org.UID, []string{"@office"})
	r.NoError(err)
	r.Len(got, 1)
	r.Equal(sealing.RecipientKindRegion, got[0].Kind)

	r.NoError(s.svc.DeletePrivateRegion(ctx, "acme", "office"))

	stored, err := sealing.StoredRegionKeys(ctx, s.dbSvc, s.org.UID)
	r.NoError(err)
	r.Empty(stored)

	retired, err := sealing.RetiredRegionKeys(ctx, s.dbSvc, s.org.UID)
	r.NoError(err)
	r.Contains(retired, credentials.RecipientFingerprint(recipient))

	_, err = s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "office"})
	r.NoError(err)

	got, err = sealing.RecipientsForRegions(ctx, s.dbSvc, s.org.UID, []string{"@office"})
	r.NoError(err)
	r.Empty(got, "no active agent and no inherited key")

	list, err := s.svc.ListPrivateRegions(ctx, "acme")
	r.NoError(err)
	r.Equal(sealing.KeyModePerAgent, list.Data[0].KeyMode)
}

// TestOrphanedRegionKeyIsNotReturned: an entry of a region that does not exist
// (left by an older version) reads as absent, and creating the region clears it.
func TestOrphanedRegionKeyIsNotReturned(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newSetup(t)
	ctx := t.Context()

	_, recipient, err := credentials.GenerateX25519Keypair()
	r.NoError(err)
	key, err := sealing.NewRegionKey(recipient, time.Now())
	r.NoError(err)
	r.NoError(sealing.SetRegionKeys(ctx, s.dbSvc, s.org.UID, map[string]sealing.RegionKey{"@gone": key}))

	keys, err := sealing.RegionKeys(ctx, s.dbSvc, s.org.UID)
	r.NoError(err)
	r.Empty(keys)

	_, err = s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "gone"})
	r.NoError(err)

	stored, err := sealing.StoredRegionKeys(ctx, s.dbSvc, s.org.UID)
	r.NoError(err)
	r.Empty(stored)

	keys, err = sealing.RegionKeys(ctx, s.dbSvc, s.org.UID)
	r.NoError(err)
	r.Empty(keys)
}

// TestRegionKeyCountsChecksNeedingReseal: a check of the region counts as
// needing a re-seal when its blob lacks the region key, does not parse, or
// was re-sealed without a second agent's confirmation; checks of another
// region, without a blob, or sealed to the key do not.
func TestRegionKeyCountsChecksNeedingReseal(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newSetup(t)
	ctx := t.Context()

	_, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "dc1"})
	r.NoError(err)

	_, recipient, err := credentials.GenerateX25519Keypair()
	r.NoError(err)
	_, other, err := credentials.GenerateX25519Keypair()
	r.NoError(err)

	sealedTo := func(to string) string {
		t.Helper()

		fps, ct, sealErr := credentials.SealForClient([]string{to}, map[string]any{"password": "x"})
		r.NoError(sealErr)

		envelope, buildErr := credentials.BuildClientSealedEnvelope(fps, ct)
		r.NoError(buildErr)

		return envelope
	}

	unconfirmed, err := credentials.MarkSealedUnconfirmed(sealedTo(recipient))
	r.NoError(err)

	add := func(slug, region string, blob *string) {
		t.Helper()

		check := models.NewCheck(s.org.UID, slug, "http")
		check.Config = models.JSONMap{"url": "https://acme.example.com"}
		check.Regions = []string{region}
		check.ConfigSealed = blob
		r.NoError(s.dbSvc.CreateCheck(ctx, check))
	}

	current, stale, garbage := sealedTo(recipient), sealedTo(other), "not an envelope"

	add("current", "@dc1", &current)         // sealed to the key: fine
	add("stale", "@dc1", &stale)             // sealed to another key
	add("garbage", "@dc1", &garbage)         // unreadable blob
	add("unconfirmed", "@dc1", &unconfirmed) // peer re-wrap, not confirmed
	add("no-blob", "@dc1", nil)              // nothing sealed
	add("elsewhere", "@dc2", &stale)         // another region

	_, err = s.svc.SetRegionKey(ctx, "acme", "dc1", &agents.SetRegionKeyRequest{Recipient: recipient})
	r.NoError(err)

	list, err := s.svc.ListPrivateRegions(ctx, "acme")
	r.NoError(err)
	r.Equal(3, list.Data[0].ChecksNeedingReseal)
}
