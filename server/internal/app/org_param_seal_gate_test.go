package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/db/sysparamseal"
	"github.com/fclairamb/solidping/server/internal/paramkeys"
	"github.com/fclairamb/solidping/server/internal/secretref"
)

type fakeWorkerLister struct {
	workers []*models.Worker
	err     error
}

func (f fakeWorkerLister) ListWorkers(context.Context) ([]*models.Worker, error) {
	return f.workers, f.err
}

var errListWorkers = errors.New("list workers boom")

func TestVersionAtLeast(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for _, version := range []string{"0.57.0", "v0.57.0", "0.57.1", "0.58.0", "1.0.0", "0.57.0-3-gabc1234-dirty"} {
		r.True(versionAtLeast(version, minSealedOrgParamWorkerVersion), version)
	}

	for _, version := range []string{"0.55.0", "v0.56.9", "0.9.0", "dev", "", "abc1234", "unknown"} {
		r.False(versionAtLeast(version, minSealedOrgParamWorkerVersion), version)
	}
}

// TestDirectWorkerOrgSealGate covers the gate of spec 2026-11-28-01: only a
// live direct-DB worker with no version or one older than 0.57.0 blocks.
func TestDirectWorkerOrgSealGate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := require.New(t)

	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	live := now.Add(-time.Minute)
	stale := now.Add(-time.Hour)

	workers := []*models.Worker{
		{Slug: "eu-west-old", Name: "eu-west-old", LastActiveAt: &live, Version: strPtr("0.55.0")},
		{Slug: "eu-west-unknown", Name: "eu-west-unknown", LastActiveAt: &live},
		{Slug: "eu-west-new", Name: "eu-west-new", LastActiveAt: &live, Version: strPtr("0.57.0")},
		{Slug: "eu-west-same", Name: "eu-west-same", LastActiveAt: &live, Version: strPtr("dev")},
		{Slug: "eu-west-stale", Name: "eu-west-stale", LastActiveAt: &stale, Version: strPtr("0.40.0")},
		{Slug: "eu-west-never", Name: "eu-west-never", Version: strPtr("0.40.0")},
		{Slug: "ag-0123456789ab", Name: "agent:acme-lan", LastActiveAt: &live, Version: strPtr("0.50.0")},
		nil,
	}

	gate := directWorkerOrgSealGate(fakeWorkerLister{workers: workers}, "dev", func() time.Time { return now })

	blockers, err := gate(ctx)
	r.NoError(err)
	r.Equal([]string{"eu-west-old (0.55.0)", "eu-west-unknown (no version)"}, blockers)

	_, err = directWorkerOrgSealGate(fakeWorkerLister{err: errListWorkers}, "dev", time.Now)(ctx)
	r.ErrorIs(err, errListWorkers)
}

// TestStartupDefersOrgParamSealForOldWorker: with a live v0.55.0 direct-DB
// worker, the startup pass leaves the secret org parameter in plaintext (and
// the boot succeeds); once the worker is upgraded, the retry tick seals it and
// the value a check resolves never changes.
func TestStartupDefersOrgParamSealForOldWorker(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := require.New(t)

	raw, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(raw.Initialize(ctx))
	t.Cleanup(func() { _ = raw.Close() })

	org := models.NewOrganization("acme-deferred-seal", "Acme")
	r.NoError(raw.CreateOrganization(ctx, org))

	const secret = "acme-plaintext-oidc-password"

	orgKey := paramkeys.StorageKey("oidc-password")
	r.NoError(raw.SetOrgParameter(ctx, org.UID, orgKey, secret, true))

	_, err = raw.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID: "0b7c1f7e-0000-4000-8000-000000000001", Slug: "eu-west-1", Name: "eu-west-1",
		Version: strPtr("0.55.0"),
	})
	r.NoError(err)

	kek := make([]byte, 32)
	_, err = rand.Read(kek)
	r.NoError(err)

	cfg := &config.Config{}
	cfg.Encryption.MasterKey = base64.StdEncoding.EncodeToString(kek)
	cfg.Encryption.AutoMigrate = true

	sysParams, err := BuildSystemParamSealer(cfg, raw)
	r.NoError(err)
	sysParams.SetOrgSealGate(directWorkerOrgSealGate(raw, "0.58.0", time.Now))

	creds, err := BuildCredentialsService(cfg, sysParams)
	r.NoError(err)

	registry := services.NewRegistry()
	registry.Credentials = creds

	server := &Server{config: cfg, dbService: sysParams, sysParams: sysParams, services: registry}
	r.NoError(server.MaybeAutoMigrateEncryption(ctx), "a deferred seal never fails the boot")

	stored, err := raw.GetOrgParameter(ctx, org.UID, orgKey)
	r.NoError(err)

	_, sealed := sysparamseal.SealedEnvelope(stored.Value)
	r.False(sealed, "the seal waits for the old direct-DB worker")
	r.True(sysParams.OrgSealPending())

	// A secret written meanwhile stays plaintext too.
	newKey := paramkeys.StorageKey("dq-key")
	r.NoError(sysParams.SetOrgParameter(ctx, org.UID, newKey, "acme-dq-key", true))

	storedNew, err := raw.GetOrgParameter(ctx, org.UID, newKey)
	r.NoError(err)
	r.Equal("acme-dq-key", storedNew.Value[models.ParameterValueKey])

	// Still gated: the retry tick seals nothing.
	r.True(server.retryDeferredOrgParamSeal(ctx))

	// The worker is upgraded.
	_, err = raw.RegisterOrUpdateWorker(ctx, &models.Worker{Slug: "eu-west-1", Version: strPtr("0.58.0")})
	r.NoError(err)

	r.False(server.retryDeferredOrgParamSeal(ctx))
	r.False(sysParams.OrgSealPending())

	for key, want := range map[string]string{"oidc-password": secret, "dq-key": "acme-dq-key"} {
		row, getErr := raw.GetOrgParameter(ctx, org.UID, paramkeys.StorageKey(key))
		r.NoError(getErr)

		_, rowSealed := sysparamseal.SealedEnvelope(row.Value)
		r.True(rowSealed, "the retry seals %s once the gate opens", key)

		resolved, lookupErr := secretref.LookupParam(ctx, sysParams, org.UID, key)
		r.NoError(lookupErr)
		r.Equal(want, resolved)
	}

	// Nothing pending: the next tick is a no-op.
	r.False(server.retryDeferredOrgParamSeal(ctx))
}
