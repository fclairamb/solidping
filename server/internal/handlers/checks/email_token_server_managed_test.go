package checks_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// mintedEmailToken is the only token shape the server mints.
var mintedEmailToken = regexp.MustCompile(`^[0-9a-f]{48}$`)

// chosenEmailToken is a well-formed token a caller tries to impose.
const chosenEmailToken = "0123456789abcdef0123456789abcdef0123456789abcdef"

// emailTokenOf reads the stored token of the org's check by slug.
func emailTokenOf(t *testing.T, rig *roundTripRig, slug string) string {
	t.Helper()

	row, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, slug)
	require.NoError(t, err)

	token, _ := row.Config["token"].(string)

	return token
}

// createMintedEmailCheck creates an email check with an empty config and returns its
// minted token.
func createMintedEmailCheck(t *testing.T, rig *roundTripRig, slug string) string {
	t.Helper()

	_, err := rig.svc.CreateCheck(t.Context(), rig.org.Slug, checks.CreateCheckRequest{
		Name: slug, Slug: slug, Type: "email", Config: map[string]any{},
	})
	require.NoError(t, err)

	token := emailTokenOf(t, rig, slug)
	require.Regexp(t, mintedEmailToken, token)

	return token
}

// TestCreateEmailCheckIgnoresCallerToken: a token in the create request is
// dropped and the server mints its own (spec 2026-11-14-01). Before the fix the
// caller's value was stored as is, so a caller could copy another org's token.
func TestCreateEmailCheckIgnoresCallerToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	rig := newRoundTripRig(t)

	victim := createMintedEmailCheck(t, rig, "victim")

	for _, chosen := range []string{chosenEmailToken, victim, "not-hex"} {
		created, err := rig.svc.CreateCheck(t.Context(), rig.org.Slug, checks.CreateCheckRequest{
			Type: "email", Config: map[string]any{"token": chosen},
		})
		r.NoError(err)

		stored := emailTokenOf(t, rig, *created.Slug)
		r.Regexp(mintedEmailToken, stored)
		r.NotEqual(chosen, stored, "the caller's token must never be stored")
		r.Equal(stored, created.Config["token"], "the response carries the minted token")
	}

	r.Equal(victim, emailTokenOf(t, rig, "victim"))
}

// TestUpdateEmailCheckKeepsStoredToken: a PATCH, a PUT-by-slug upsert and a
// token-less PATCH all leave the stored token alone.
func TestUpdateEmailCheckKeepsStoredToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	rig := newRoundTripRig(t)

	victim := createMintedEmailCheck(t, rig, "victim")
	mine := createMintedEmailCheck(t, rig, "mine")

	patch := map[string]any{"token": victim, "require_dkim": true}
	_, err := rig.svc.UpdateCheck(t.Context(), rig.org.Slug, "mine", &checks.UpdateCheckRequest{Config: &patch})
	r.NoError(err)
	r.Equal(mine, emailTokenOf(t, rig, "mine"), "a PATCH must not take a caller's token")

	_, _, err = rig.svc.UpsertCheck(t.Context(), rig.org.Slug, "mine", &checks.UpsertCheckRequest{
		Name: "mine", Type: "email", Config: map[string]any{"token": chosenEmailToken},
	})
	r.NoError(err)
	r.Equal(mine, emailTokenOf(t, rig, "mine"), "an upsert must not take a caller's token")

	empty := map[string]any{"require_spf": "pass"}
	_, err = rig.svc.UpdateCheck(t.Context(), rig.org.Slug, "mine", &checks.UpdateCheckRequest{Config: &empty})
	r.NoError(err)
	r.Equal(mine, emailTokenOf(t, rig, "mine"), "a PATCH without token keeps it")

	r.Equal(victim, emailTokenOf(t, rig, "victim"))
}

// TestCloneEmailCheckMintsFreshToken: a clone gets its own inbound address.
func TestCloneEmailCheckMintsFreshToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	rig := newRoundTripRig(t)

	source := createMintedEmailCheck(t, rig, "source")

	clone, err := rig.svc.CloneCheck(t.Context(), rig.org.Slug, "source", &checks.CloneCheckRequest{})
	r.NoError(err)

	cloned := emailTokenOf(t, rig, *clone.Slug)
	r.Regexp(mintedEmailToken, cloned)
	r.NotEqual(source, cloned)
	r.Equal(source, emailTokenOf(t, rig, "source"))
}

// TestImportAndApplyNeverTakeDocumentToken: a document's token for a NEW email
// check is replaced by a minted one, and an existing check keeps its own token
// across an apply whatever the document carries.
func TestImportAndApplyNeverTakeDocumentToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	rig := newRoundTripRig(t)

	victim := createMintedEmailCheck(t, rig, "victim")
	existing := createMintedEmailCheck(t, rig, "existing")

	doc := &checks.ExportDocument{
		Version: 1,
		Checks: []checks.ExportCheck{
			{Name: "imported", Slug: "imported", Type: "email", Config: map[string]any{"token": victim}},
		},
	}
	result, err := rig.svc.ImportChecks(t.Context(), rig.org.Slug, doc, false)
	r.NoError(err)
	r.Empty(result.Errors)

	imported := emailTokenOf(t, rig, "imported")
	r.Regexp(mintedEmailToken, imported)
	r.NotEqual(victim, imported, "an import must mint a fresh token for a new check")

	manifest := &checks.ExportDocument{
		Version: 1,
		Checks: []checks.ExportCheck{
			{Name: "existing", Slug: "existing", Type: "email", Config: map[string]any{"token": chosenEmailToken}},
			{Name: "applied", Slug: "applied", Type: "email", Config: map[string]any{"token": victim}},
		},
	}
	applied, err := rig.svc.ApplyChecks(t.Context(), rig.org.Slug, manifest, checks.ApplyOptions{})
	r.NoError(err)
	r.Empty(applied.Errors)

	r.Equal(existing, emailTokenOf(t, rig, "existing"), "an apply must keep the existing check's token")

	fresh := emailTokenOf(t, rig, "applied")
	r.Regexp(mintedEmailToken, fresh)
	r.NotEqual(victim, fresh)

	r.Equal(victim, emailTokenOf(t, rig, "victim"))
}
