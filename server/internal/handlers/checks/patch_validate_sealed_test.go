package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// TestSealedOnlyPatchWithRequiredPasswordSecretUnrelatedFieldSucceeds is the
// sealed-only trap from the spec: SFTP requires password OR private_key
// (checksftp/config.go SFTPConfig.Validate). A sealed-only check's merged
// PATCH config can't carry that secret (the server cannot decrypt what it
// never held), so without placeholder injection an unrelated PATCH (here,
// just the path) would falsely 400 with "password or private_key is
// required" — a real regression on a valid, unrelated edit.
func TestSealedOnlyPatchWithRequiredPasswordSecretUnrelatedFieldSucceeds(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupEncryptedChecksService(t)
	ctx := t.Context()

	enrollSealAgent(t, dbSvc, org.UID, "dc1-agent")

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:    "sealed-sftp-password",
		Slug:    "sealed-sftp-password",
		Type:    "sftp",
		Regions: []string{sealTestRegion},
		Config: map[string]any{
			"host":     "sftp.internal.example.com",
			"username": "alice",
			"password": "hunter2",
		},
	})
	r.NoError(err)

	before, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotNil(before.ConfigSealed, "must be sealed")
	r.Nil(before.ConfigPrivate, "must be sealed-ONLY: no server-decryptable copy")
	originalBlob := *before.ConfigSealed

	// Unrelated PATCH: no secret fields in the request at all.
	patch := map[string]any{
		"host": "sftp.internal.example.com", "username": "alice", "path": "/new/path",
	}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &patch})
	r.NoError(err, "an unrelated PATCH on a sealed-only check with a required secret must not be rejected")

	after, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Equal("/new/path", after.Config["path"])
	r.Equal(originalBlob, *after.ConfigSealed, "the sealed blob must be kept AS-IS")
	r.Nil(after.ConfigPrivate, "still sealed-only")
}

// TestSealedOnlyPatchWithRequiredPrivateKeySecretUnrelatedFieldSucceeds is the
// private_key variant of the trap above. checksftp's Validate PEM-decodes
// private_key (validatePrivateKey in config.go) — a bare-string placeholder
// would fail that decode with "invalid PEM format" and produce the exact
// false-rejection the spec warns about. This proves the PEM-shaped
// placeholder path.
func TestSealedOnlyPatchWithRequiredPrivateKeySecretUnrelatedFieldSucceeds(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupEncryptedChecksService(t)
	ctx := t.Context()

	enrollSealAgent(t, dbSvc, org.UID, "dc1-agent")

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:    "sealed-sftp-privatekey",
		Slug:    "sealed-sftp-privatekey",
		Type:    "sftp",
		Regions: []string{sealTestRegion},
		Config: map[string]any{
			"host":        "sftp.internal.example.com",
			"username":    "alice",
			"private_key": validSSHPrivateKeyPEM(t),
		},
	})
	r.NoError(err)

	before, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.NotNil(before.ConfigSealed)
	r.Nil(before.ConfigPrivate)
	r.Contains(privateKeys(t, before), "private_key")
	originalBlob := *before.ConfigSealed

	patch := map[string]any{
		"host": "sftp.internal.example.com", "username": "alice", "path": "/new/path",
	}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &patch})
	r.NoError(err,
		"an unrelated PATCH on a sealed-only check whose secret is private_key must not be "+
			"rejected by the PEM-decode check running against a placeholder")

	after, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Equal("/new/path", after.Config["path"])
	r.Equal(originalBlob, *after.ConfigSealed, "the sealed blob must be kept AS-IS")
}

// TestPlaintextPatchClearingRequiredSecretStillRejected is the negative proof
// that placeholder injection is scoped to sealed-only checks: on a
// plaintext-envelope row (server CAN decrypt), an explicit PATCH that clears
// the only secret SFTP has (password, no private_key) must still hit the
// checker's real "is required" error — never silently masked by a
// placeholder.
func TestPlaintextPatchClearingRequiredSecretStillRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, _, _, org := setupPlaintextChecksService(t)
	ctx := t.Context()

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "plaintext-sftp",
		Slug: "plaintext-sftp",
		Type: "sftp",
		Config: map[string]any{
			"host":     "sftp.example.com",
			"username": "alice",
			"password": "hunter2",
		},
	})
	r.NoError(err)

	// Explicitly clear the password, without providing a private_key either.
	patch := map[string]any{"host": "sftp.example.com", "username": "alice", "password": ""}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &patch})
	r.Error(err, "clearing the only secret a required-secret checker has must still fail validation")

	configErr := checkerdef.IsConfigError(err)
	r.NotNil(configErr, "must be a structured ConfigError, mapping to a 400 at the handler")
	r.Equal("password", configErr.Parameter)
}

// TestPlaintextPatchUnrelatedFieldWithSecretPresentSucceeds is the "otherwise
// unchanged" half of the plaintext/AES-envelope contract: the server CAN
// decrypt this row, so the merged config carries the real secret (not a
// placeholder), and an unrelated PATCH against the full, real config
// succeeds exactly as before this change.
func TestPlaintextPatchUnrelatedFieldWithSecretPresentSucceeds(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, creds, org := setupPlaintextChecksService(t)
	ctx := t.Context()

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "plaintext-sftp-unrelated",
		Slug: "plaintext-sftp-unrelated",
		Type: "sftp",
		Config: map[string]any{
			"host":     "sftp.example.com",
			"username": "alice",
			"password": "hunter2",
		},
	})
	r.NoError(err)

	patch := map[string]any{"host": "sftp.example.com", "username": "alice", "path": "/data"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &patch})
	r.NoError(err)

	after, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Equal("/data", after.Config["path"])
	r.NotNil(after.ConfigPrivate)

	private, err := creds.DecryptForOrg(ctx, org.UID, *after.ConfigPrivate)
	r.NoError(err)
	r.Equal("hunter2", private["password"], "the real secret, not a placeholder, must have been preserved")
}
