package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// TestSealedOnlyPatchWithSecretHeadersUnrelatedFieldSucceeds is the
// map-shaped variant of the sealed-only trap: checkhttp.SecretHeaders is a
// map[string]string ([config.go:295]), so the placeholder injected for a key
// absent from a sealed-only check's merged PATCH config must be an empty map,
// never a plain string — HTTPConfig.FromMap rejects a string outright with
// "secretHeaders: must be a map[string]string".
func TestSealedOnlyPatchWithSecretHeadersUnrelatedFieldSucceeds(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupEncryptedChecksService(t)
	ctx := t.Context()

	enrollSealAgent(t, dbSvc, org.UID, "dc1-agent")

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:    "sealed-http-secretheaders",
		Slug:    "sealed-http-secretheaders",
		Type:    "http",
		Regions: []string{sealTestRegion},
		Config: map[string]any{
			"url":           "https://internal.example.com/health",
			"secretHeaders": map[string]any{"X-Api-Key": "hunter2"},
		},
	})
	r.NoError(err)

	before, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotNil(before.ConfigSealed, "must be sealed")
	r.Nil(before.ConfigPrivate, "must be sealed-ONLY: no server-decryptable copy")
	r.Contains(privateKeys(t, before), "secretHeaders")
	originalBlob := *before.ConfigSealed

	// Unrelated PATCH: no secretHeaders in the request at all.
	patch := map[string]any{"url": "https://internal.example.com/health2"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &patch})
	r.NoError(err,
		"an unrelated PATCH on a sealed-only check carrying secretHeaders must not be rejected "+
			"by the checker's own map-shape validation running against a placeholder")

	after, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Equal("https://internal.example.com/health2", after.Config["url"])
	r.Equal(originalBlob, *after.ConfigSealed, "the sealed blob must be kept AS-IS")
	r.Nil(after.ConfigPrivate, "still sealed-only")
	r.NotContains(after.Config, "secretHeaders", "the public config must never carry the secret")
}

// TestSealedOnlyPatchWithSecretMetadataUnrelatedFieldSucceeds is the gRPC
// counterpart: checkgrpc.SecretMetadata is also map[string]string
// ([config.go:132]).
func TestSealedOnlyPatchWithSecretMetadataUnrelatedFieldSucceeds(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupEncryptedChecksService(t)
	ctx := t.Context()

	enrollSealAgent(t, dbSvc, org.UID, "dc1-agent")

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:    "sealed-grpc-secretmetadata",
		Slug:    "sealed-grpc-secretmetadata",
		Type:    "grpc",
		Regions: []string{sealTestRegion},
		Config: map[string]any{
			"host":           "grpc.internal.example.com",
			"secretMetadata": map[string]any{"authorization": "bearer-token"},
		},
	})
	r.NoError(err)

	before, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotNil(before.ConfigSealed, "must be sealed")
	r.Nil(before.ConfigPrivate, "must be sealed-ONLY: no server-decryptable copy")
	r.Contains(privateKeys(t, before), "secretMetadata")
	originalBlob := *before.ConfigSealed

	patch := map[string]any{"host": "grpc2.internal.example.com"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &patch})
	r.NoError(err,
		"an unrelated PATCH on a sealed-only check carrying secretMetadata must not be rejected")

	after, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Equal("grpc2.internal.example.com", after.Config["host"])
	r.Equal(originalBlob, *after.ConfigSealed, "the sealed blob must be kept AS-IS")
	r.Nil(after.ConfigPrivate, "still sealed-only")
}

// TestSealedOnlyPatchWithSecretsMapUnrelatedFieldSucceeds is the JS
// counterpart: checkjs.Secrets is also map[string]string
// ([config.go:63]), added by spec 2026-09-11-05.
func TestSealedOnlyPatchWithSecretsMapUnrelatedFieldSucceeds(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupEncryptedChecksService(t)
	ctx := t.Context()

	enrollSealAgent(t, dbSvc, org.UID, "dc1-agent")

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:    "sealed-js-secrets",
		Slug:    "sealed-js-secrets",
		Type:    "js",
		Regions: []string{sealTestRegion},
		Config: map[string]any{
			"script":  "return true;",
			"secrets": map[string]any{"apiToken": "hunter2"},
		},
	})
	r.NoError(err)

	before, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotNil(before.ConfigSealed, "must be sealed")
	r.Nil(before.ConfigPrivate, "must be sealed-ONLY: no server-decryptable copy")
	r.Contains(privateKeys(t, before), "secrets")
	originalBlob := *before.ConfigSealed

	patch := map[string]any{"script": "return false;"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &patch})
	r.NoError(err,
		"an unrelated PATCH on a sealed-only check carrying secrets must not be rejected")

	after, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Equal("return false;", after.Config["script"])
	r.Equal(originalBlob, *after.ConfigSealed, "the sealed blob must be kept AS-IS")
	r.Nil(after.ConfigPrivate, "still sealed-only")
}
