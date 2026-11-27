package sysparamseal_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sysparamseal"
	"github.com/fclairamb/solidping/server/internal/paramkeys"
	"github.com/fclairamb/solidping/server/internal/secretref"
)

// runOrgSuite covers the secret organization parameters of spec 2026-11-27-02,
// on the database runSuite was given. Each case owns its organization.
func runOrgSuite(t *testing.T, raw db.Service, key []byte) {
	t.Helper()

	t.Run("OrgSecretParameterIsSealed", func(t *testing.T) { testOrgSecretIsSealed(t, raw, key) })
	t.Run("OrgPlainAndPlatformRowsStayPlain", func(t *testing.T) { testOrgPlainRowsStayPlain(t, raw, key) })
	t.Run("OrgWrongMasterKeyFailsClosed", func(t *testing.T) { testOrgWrongKeyFailsClosed(t, raw, key) })
	t.Run("OrgMigrationSealsPlaintextSecrets", func(t *testing.T) { testOrgMigration(t, raw, key) })
}

func newOrg(t *testing.T, raw db.Service, slug string) string {
	t.Helper()

	org := models.NewOrganization(slug, "Acme "+slug)
	require.NoError(t, raw.CreateOrganization(t.Context(), org))

	return org.UID
}

// rawOrgValue reads the stored JSON of an org row, bypassing the decorator.
func rawOrgValue(t *testing.T, svc db.Service, orgUID, key string) string {
	t.Helper()

	var value string
	require.NoError(t, svc.DB().NewRaw(
		"SELECT CAST(value AS TEXT) FROM parameters "+
			"WHERE key = ? AND organization_uid = ? AND deleted_at IS NULL",
		key, orgUID,
	).Scan(context.Background(), &value))

	return value
}

func findParam(params []*models.Parameter, key string) *models.Parameter {
	for _, param := range params {
		if param.Key == key {
			return param
		}
	}

	return nil
}

func testOrgSecretIsSealed(t *testing.T, raw db.Service, key []byte) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)
	svc := wrap(t, raw, key)
	orgUID := newOrg(t, raw, "acme-org-sealed")
	storageKey := paramkeys.StorageKey("x")

	r.NoError(svc.SetOrgParameter(ctx, orgUID, storageKey, "s3cret", true))

	stored := rawOrgValue(t, raw, orgUID, storageKey)
	r.NotContains(stored, "s3cret")
	r.Contains(stored, sysparamseal.SealedMarkerKey)

	rawParam, err := svc.Raw().GetOrgParameter(ctx, orgUID, storageKey)
	r.NoError(err)

	_, sealed := sysparamseal.SealedEnvelope(rawParam.Value)
	r.True(sealed, "the raw row must hold an envelope")

	param, err := svc.GetOrgParameter(ctx, orgUID, storageKey)
	r.NoError(err)
	r.Equal("s3cret", param.Value[models.ParameterValueKey])

	listed, err := svc.ListOrgParameters(ctx, orgUID)
	r.NoError(err)
	r.Equal("s3cret", findParam(listed, storageKey).Value[models.ParameterValueKey])

	byKey, err := svc.ListOrgParametersByKey(ctx, storageKey)
	r.NoError(err)
	r.Equal("s3cret", findParam(byKey, storageKey).Value[models.ParameterValueKey])

	// `${param:x}` resolves through the decorated service, so a check using it runs.
	resolved, err := secretref.LookupParam(ctx, svc, orgUID, "x")
	r.NoError(err)
	r.Equal("s3cret", resolved)

	// Secret to non-secret rewrites the row in clear on the same call…
	r.NoError(svc.SetOrgParameter(ctx, orgUID, storageKey, "now-public", false))
	r.Contains(rawOrgValue(t, raw, orgUID, storageKey), "now-public")

	// …and back to secret seals it again.
	r.NoError(svc.SetOrgParameter(ctx, orgUID, storageKey, "secret-again", true))
	r.NotContains(rawOrgValue(t, raw, orgUID, storageKey), "secret-again")

	resolved, err = secretref.LookupParam(ctx, svc, orgUID, "x")
	r.NoError(err)
	r.Equal("secret-again", resolved)
}

func testOrgPlainRowsStayPlain(t *testing.T, raw db.Service, key []byte) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)
	svc := wrap(t, raw, key)
	orgUID := newOrg(t, raw, "acme-org-plain")

	// A non-secret org parameter: the API returns its value anyway.
	plainKey := paramkeys.StorageKey("region-label")
	r.NoError(svc.SetOrgParameter(ctx, orgUID, plainKey, "paris", false))
	r.Contains(rawOrgValue(t, raw, orgUID, plainKey), "paris")

	// A platform row outside `usr.`, even flagged secret, is left alone.
	const platformKey = "custom_regions"

	r.NoError(svc.SetOrgParameter(ctx, orgUID, platformKey, "acme-platform-value", true))
	stored := rawOrgValue(t, raw, orgUID, platformKey)
	r.Contains(stored, "acme-platform-value")
	r.NotContains(stored, sysparamseal.SealedMarkerKey)

	param, err := svc.GetOrgParameter(ctx, orgUID, platformKey)
	r.NoError(err)
	r.Equal("acme-platform-value", param.Value[models.ParameterValueKey])

	r.False(sysparamseal.ShouldSealOrgParameter(platformKey, true))
	r.False(sysparamseal.ShouldSealOrgParameter(plainKey, false))
	r.True(sysparamseal.ShouldSealOrgParameter(plainKey, true))
}

func testOrgWrongKeyFailsClosed(t *testing.T, raw db.Service, key []byte) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)
	orgUID := newOrg(t, raw, "acme-org-wrongkey")
	storageKey := paramkeys.StorageKey("api-token")

	r.NoError(wrap(t, raw, key).SetOrgParameter(ctx, orgUID, storageKey, "acme-token", true))

	for name, svc := range map[string]*sysparamseal.Service{
		"wrong key": wrap(t, raw, newMasterKey(t)),
		"no key":    sysparamseal.Wrap(raw, nil, nil),
	} {
		param, err := svc.GetOrgParameter(ctx, orgUID, storageKey)
		r.ErrorIs(err, sysparamseal.ErrSealedUnavailable, name)
		r.Nil(param, name)

		listed, err := svc.ListOrgParameters(ctx, orgUID)
		r.ErrorIs(err, sysparamseal.ErrSealedUnavailable, name)
		r.Nil(listed, name)

		resolved, err := secretref.LookupParam(ctx, svc, orgUID, "api-token")
		r.ErrorIs(err, secretref.ErrUnresolved, name)
		r.Empty(resolved, name)
	}
}

func testOrgMigration(t *testing.T, raw db.Service, key []byte) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)
	orgUID := newOrg(t, raw, "acme-org-migrate")

	legacy := map[string]string{"alpha": "acme-alpha-secret", "beta": "acme-beta-secret"}
	for name, value := range legacy {
		r.NoError(raw.SetOrgParameter(ctx, orgUID, paramkeys.StorageKey(name), value, true))
	}

	r.NoError(raw.SetOrgParameter(ctx, orgUID, paramkeys.StorageKey("gamma"), "acme-gamma-public", false))

	// Without a master key the rows are counted for the warning, never sealed.
	keyless := sysparamseal.Wrap(raw, nil, nil)

	count, err := keyless.PlaintextSecretOrgParameterCount(ctx)
	r.NoError(err)
	r.GreaterOrEqual(count, len(legacy))

	sealed, err := keyless.SealPlaintextOrgParameters(ctx)
	r.NoError(err)
	r.Zero(sealed)

	svc := wrap(t, raw, key)

	sealed, err = svc.SealPlaintextOrgParameters(ctx)
	r.NoError(err)
	r.GreaterOrEqual(sealed, len(legacy))

	for name, value := range legacy {
		r.NotContains(rawOrgValue(t, raw, orgUID, paramkeys.StorageKey(name)), value)

		resolved, lookupErr := secretref.LookupParam(ctx, svc, orgUID, name)
		r.NoError(lookupErr)
		r.Equal(value, resolved)
	}

	r.Contains(rawOrgValue(t, raw, orgUID, paramkeys.StorageKey("gamma")), "acme-gamma-public")

	// A second boot seals nothing.
	sealed, err = svc.SealPlaintextOrgParameters(ctx)
	r.NoError(err)
	r.Zero(sealed)

	count, err = svc.PlaintextSecretOrgParameterCount(ctx)
	r.NoError(err)
	r.Zero(count)
}
