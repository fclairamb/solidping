package sealing_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/sealing"
)

func newStore(t *testing.T) (*sqlite.Service, *models.Organization) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	return dbSvc, org
}

func newRecipient(t *testing.T) string {
	t.Helper()

	_, recipient, err := credentials.GenerateX25519Keypair()
	require.NoError(t, err)

	return recipient
}

// TestRegionKeysDropsKeysOfDeletedRegions: a key left behind by a deleted
// region is not returned, but is still listed by StoredRegionKeys.
func TestRegionKeysDropsKeysOfDeletedRegions(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	store, org := newStore(t)

	// No keys at all.
	keys, err := sealing.RegionKeys(ctx, store, org.UID)
	r.NoError(err)
	r.Empty(keys)

	live, err := sealing.NewRegionKey(newRecipient(t), time.Now())
	r.NoError(err)
	gone, err := sealing.NewRegionKey(newRecipient(t), time.Now())
	r.NoError(err)

	r.NoError(sealing.SetRegionKeys(ctx, store, org.UID, map[string]sealing.RegionKey{
		"@dc1": live, "@gone": gone,
	}))

	// Keys but no region row: every key belongs to a deleted region.
	keys, err = sealing.RegionKeys(ctx, store, org.UID)
	r.NoError(err)
	r.Empty(keys)

	r.NoError(store.SetOrgParameter(ctx, org.UID, regions.ParamCustomRegions,
		[]regions.RegionDefinition{{Slug: "dc1", Name: "DC1"}}, false))

	keys, err = sealing.RegionKeys(ctx, store, org.UID)
	r.NoError(err)
	r.Len(keys, 1)
	r.Equal(live.Recipient, keys["@dc1"].Recipient)

	stored, err := sealing.StoredRegionKeys(ctx, store, org.UID)
	r.NoError(err)
	r.Len(stored, 2)

	// A malformed region row reads as no region.
	r.NoError(store.SetOrgParameter(ctx, org.UID, regions.ParamCustomRegions, "not a list", false))

	keys, err = sealing.RegionKeys(ctx, store, org.UID)
	r.NoError(err)
	r.Empty(keys)
}

// TestStoredRegionKeysMalformed: malformed rows read as none and invalid
// recipients are dropped.
func TestStoredRegionKeysMalformed(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	store, org := newStore(t)

	r.NoError(store.SetOrgParameter(ctx, org.UID, sealing.ParamKeyRegionKeys, "garbage", false))

	keys, err := sealing.StoredRegionKeys(ctx, store, org.UID)
	r.NoError(err)
	r.Empty(keys)

	r.NoError(store.SetOrgParameter(ctx, org.UID, sealing.ParamKeyRegionKeys, map[string]sealing.RegionKey{
		"@dc1": {Recipient: "not-a-recipient"}, "@dc2": {},
	}, false))

	keys, err = sealing.StoredRegionKeys(ctx, store, org.UID)
	r.NoError(err)
	r.Empty(keys)

	r.NoError(sealing.SetRegionKeys(ctx, store, org.UID, nil))
	param, err := store.GetOrgParameter(ctx, org.UID, sealing.ParamKeyRegionKeys)
	r.NoError(err)
	r.Nil(param)
}

func TestRetiredRegionKeys(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	store, org := newStore(t)

	retired, err := sealing.RetiredRegionKeys(ctx, store, org.UID)
	r.NoError(err)
	r.Empty(retired)

	// Malformed row.
	r.NoError(store.SetOrgParameter(ctx, org.UID, sealing.ParamKeyRetiredRegionKeys, "garbage", false))

	retired, err = sealing.RetiredRegionKeys(ctx, store, org.UID)
	r.NoError(err)
	r.Empty(retired)

	// Entries whose fingerprint does not match their recipient are dropped.
	good := newRecipient(t)
	r.NoError(store.SetOrgParameter(ctx, org.UID, sealing.ParamKeyRetiredRegionKeys, map[string]sealing.RegionKey{
		credentials.RecipientFingerprint(good): {Recipient: good},
		"deadbeef":                             {Recipient: good},
		"empty":                                {},
	}, false))

	retired, err = sealing.RetiredRegionKeys(ctx, store, org.UID)
	r.NoError(err)
	r.Len(retired, 1)
	r.Contains(retired, credentials.RecipientFingerprint(good))
}

// TestRetireRegionKeyKeepsTheMostRecent: the list is bounded and the oldest go first.
func TestRetireRegionKeyKeepsTheMostRecent(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	store, org := newStore(t)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	var first string

	const total = 66

	for i := range total {
		recipient := newRecipient(t)
		if i == 0 {
			first = credentials.RecipientFingerprint(recipient)
		}

		key, err := sealing.NewRegionKey(recipient, base)
		r.NoError(err)
		r.NoError(sealing.RetireRegionKey(ctx, store, org.UID, key, base.Add(time.Duration(i)*time.Hour)))
	}

	retired, err := sealing.RetiredRegionKeys(ctx, store, org.UID)
	r.NoError(err)
	r.Len(retired, 64)
	r.NotContains(retired, first)
}

func TestKnownRegionRecipients(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	store, org := newStore(t)

	current, err := sealing.NewRegionKey(newRecipient(t), time.Now())
	r.NoError(err)
	old, err := sealing.NewRegionKey(newRecipient(t), time.Now())
	r.NoError(err)

	r.NoError(sealing.SetRegionKeys(ctx, store, org.UID, map[string]sealing.RegionKey{"@dc1": current}))
	r.NoError(sealing.RetireRegionKey(ctx, store, org.UID, old, time.Now()))

	known, err := sealing.KnownRegionRecipients(ctx, store, org.UID)
	r.NoError(err)
	r.Equal(map[string]string{
		credentials.RecipientFingerprint(current.Recipient): current.Recipient,
		credentials.RecipientFingerprint(old.Recipient):     old.Recipient,
	}, known)
}

func TestNewRegionKeyRejectsInvalidRecipient(t *testing.T) {
	t.Parallel()

	_, err := sealing.NewRegionKey("nope", time.Now())
	require.Error(t, err)
}

// failingStore fails every parameter read, or only those of the failKey.
type failingStore struct {
	sealing.Store
	failKey string
}

func (f failingStore) GetOrgParameter(ctx context.Context, orgUID, key string) (*models.Parameter, error) {
	if f.failKey == "" || f.failKey == key {
		return nil, errStore
	}

	return f.Store.GetOrgParameter(ctx, orgUID, key)
}

var errStore = errors.New("store down")

func TestStoreReadFailuresArePropagated(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	store, org := newStore(t)

	key, err := sealing.NewRegionKey(newRecipient(t), time.Now())
	r.NoError(err)
	r.NoError(sealing.SetRegionKeys(ctx, store, org.UID, map[string]sealing.RegionKey{"@dc1": key}))

	// Region keys read fine, the custom regions read fails.
	_, err = sealing.RegionKeys(ctx, failingStore{Store: store, failKey: regions.ParamCustomRegions}, org.UID)
	r.ErrorIs(err, errStore)

	_, err = sealing.RegionKeys(ctx, failingStore{Store: store}, org.UID)
	r.ErrorIs(err, errStore)

	_, err = sealing.RetiredRegionKeys(ctx, failingStore{Store: store}, org.UID)
	r.ErrorIs(err, errStore)

	err = sealing.RetireRegionKey(ctx, failingStore{Store: store}, org.UID, key, time.Now())
	r.ErrorIs(err, errStore)

	_, err = sealing.KnownRegionRecipients(ctx, failingStore{Store: store}, org.UID)
	r.ErrorIs(err, errStore)

	_, err = sealing.KnownRegionRecipients(ctx,
		failingStore{Store: store, failKey: sealing.ParamKeyRetiredRegionKeys}, org.UID)
	r.ErrorIs(err, errStore)
}
