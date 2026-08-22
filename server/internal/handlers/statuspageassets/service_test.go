package statuspageassets_test

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
	"github.com/fclairamb/solidping/server/internal/handlers/statuspageassets"
	"github.com/fclairamb/solidping/server/internal/handlers/statuspages"
)

// pngBytes is a one-pixel PNG. Content is irrelevant to this package (it never
// decodes the image), but a plausible blob keeps the fixtures honest.
const pngBytes = "\x89PNG\r\n\x1a\n-not-really-a-png-but-opaque-bytes"

type assetSetup struct {
	dbSvc db.Service
	svc   *statuspageassets.Service
	files *files.Service
	org   *models.Organization
	page  *models.StatusPage
}

// openPublic exercises the REAL public read path: the org-agnostic, live-rows
// only lookup gated by the topic allowlist. It is what /pub/assets/:uid calls,
// so a test asserting "this blob is (not) public" asserts the shipped rule and
// not a paraphrase of it.
func (s *assetSetup) openPublic(t *testing.T, fileUID string) (*models.File, io.ReadCloser, error) {
	t.Helper()

	file, err := s.files.GetPublicFile(t.Context(), fileUID)
	if err != nil {
		return nil, nil, err
	}

	body, err := s.files.OpenContent(t.Context(), file)
	if err != nil {
		return nil, nil, err
	}

	return file, body, nil
}

// logoUID reads the page's logo slot out of settings, failing the test when
// it is unset.
func logoUID(t *testing.T, page *models.StatusPage) string {
	t.Helper()
	require.NotNil(t, page.Settings.LogoFileUID())

	return *page.Settings.LogoFileUID()
}

func newAssetSetup(t *testing.T) *assetSetup {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	page := models.NewStatusPage(org.UID, "Acme Status", "acme-status")
	r.NoError(dbSvc.CreateStatusPage(ctx, page))

	// The local-FS backend self-registers on demand; the production wiring
	// does this once in server.go.
	localfs.Register()

	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "test-secret"
	cfg.FileStorage.Type = "local"
	cfg.FileStorage.LocalRoot = t.TempDir()

	filesSvc := files.NewService(dbSvc, cfg)

	return &assetSetup{
		dbSvc: dbSvc,
		svc:   statuspageassets.NewService(dbSvc, filesSvc),
		files: filesSvc,
		org:   org,
		page:  page,
	}
}

func upload(body string) statuspageassets.Upload {
	return statuspageassets.Upload{
		Name:     "brand.png",
		MIMEType: "image/png",
		Size:     int64(len(body)),
		Body:     strings.NewReader(body),
	}
}

// TestUploadPointsThePageAtTheFileAndServesIt is the happy path for both slots:
// the settings section gets the file UID, the public URL is derived from it,
// the file carries the topic that authorizes it, and the unsigned public route
// resolves the blob back.
func TestUploadPointsThePageAtTheFileAndServesIt(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newAssetSetup(t)

	page, err := s.svc.Upload(t.Context(), "acme", "acme-status", statuspageassets.KindLogo, upload(pngBytes), nil)
	r.NoError(err)
	r.NotNil(page.Settings.LogoFileUID())
	r.Nil(page.Settings.FaviconFileUID(), "uploading a logo must not touch the favicon slot")

	uid := logoUID(t, page)

	url := statuspageassets.PublicURL(page.Settings.LogoFileUID())
	r.NotNil(url)
	r.Equal(statuspageassets.PublicPathPrefix+uid, *url)

	// The topic IS the authorization — assert it explicitly, since a missing
	// topic would make the blob private and 404 the page's own logo.
	stored, err := s.dbSvc.GetFile(t.Context(), s.org.UID, uid)
	r.NoError(err)
	r.NotNil(stored.Topic)
	r.Equal(files.StatusPageAssetTopic(page.UID, "logo"), *stored.Topic)
	r.True(files.IsPublicTopic(*stored.Topic))

	file, body, err := s.openPublic(t, uid)
	r.NoError(err)

	defer func() { _ = body.Close() }()

	r.Equal("image/png", file.MimeType)

	got, err := io.ReadAll(body)
	r.NoError(err)
	r.Equal(pngBytes, string(got))
}

// TestFaviconSlotIsIndependent proves the two keys really are separate slots:
// writing one leaves the other alone, in both directions. It is the section-level
// half of the clobber property — the whole-section write has to RESTATE the
// slot it is not touching.
func TestFaviconSlotIsIndependent(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newAssetSetup(t)

	_, err := s.svc.Upload(t.Context(), "acme", "acme-status", statuspageassets.KindLogo, upload(pngBytes), nil)
	r.NoError(err)

	page, err := s.svc.Upload(t.Context(), "acme", "acme-status", statuspageassets.KindFavicon, upload("icon"), nil)
	r.NoError(err)
	r.NotNil(page.Settings.LogoFileUID())
	r.NotNil(page.Settings.FaviconFileUID())
	r.NotEqual(*page.Settings.LogoFileUID(), *page.Settings.FaviconFileUID())

	page, err = s.svc.Clear(t.Context(), "acme", "acme-status", statuspageassets.KindFavicon)
	r.NoError(err)
	r.NotNil(page.Settings.LogoFileUID(), "clearing the favicon must not clear the logo")
	r.Nil(page.Settings.FaviconFileUID())
}

// TestReplacingAnAssetUnpublishesTheOldBlob is the security property of the
// unsigned public route: the replaced file row is soft-deleted, and the public
// read is live-rows-only, so the old blob stops being served in the same write.
// That soft delete is now the ENTIRE un-publish mechanism for a replace.
func TestReplacingAnAssetUnpublishesTheOldBlob(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newAssetSetup(t)

	first, err := s.svc.Upload(t.Context(), "acme", "acme-status", statuspageassets.KindLogo, upload("one"), nil)
	r.NoError(err)
	oldUID := logoUID(t, first)

	second, err := s.svc.Upload(t.Context(), "acme", "acme-status", statuspageassets.KindLogo, upload("two"), nil)
	r.NoError(err)
	r.NotEqual(oldUID, logoUID(t, second))

	_, _, err = s.openPublic(t, oldUID)
	r.ErrorIs(err, files.ErrFileNotFound, "the replaced blob must stop resolving")

	_, body, err := s.openPublic(t, logoUID(t, second))
	r.NoError(err)
	r.NoError(body.Close())
}

// TestClearingAnAssetUnpublishesTheBlob is the same property for an explicit
// removal with no replacement.
func TestClearingAnAssetUnpublishesTheBlob(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newAssetSetup(t)

	page, err := s.svc.Upload(t.Context(), "acme", "acme-status", statuspageassets.KindLogo, upload(pngBytes), nil)
	r.NoError(err)
	uid := logoUID(t, page)

	cleared, err := s.svc.Clear(t.Context(), "acme", "acme-status", statuspageassets.KindLogo)
	r.NoError(err)
	r.Nil(cleared.Settings.LogoFileUID())

	_, _, err = s.openPublic(t, uid)
	r.ErrorIs(err, files.ErrFileNotFound)
}

// TestDisabledPageStillServesItsAssets records a DELIBERATE loosening
// (spec 2026-08-22-03), rather than leaving it to be discovered later as a leak.
//
// Under the old state-based check, disabling a page took its logo offline with
// it. Authorization now comes from the file's own topic, so it does not. That
// is accepted because the URL embeds an unguessable UUIDv4 (capability-like,
// not enumerable) and a brand logo is not a secret. The operator-facing
// consequence — "disable" is not "un-publish the logo" — is stated in the
// docs-site status-page page.
func TestDisabledPageStillServesItsAssets(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newAssetSetup(t)

	page, err := s.svc.Upload(t.Context(), "acme", "acme-status", statuspageassets.KindLogo, upload(pngBytes), nil)
	r.NoError(err)
	uid := logoUID(t, page)

	disabled := false
	r.NoError(s.dbSvc.UpdateStatusPage(t.Context(), page.UID, &models.StatusPageUpdate{Enabled: &disabled}))

	_, body, err := s.openPublic(t, uid)
	r.NoError(err, "disabling a page no longer un-publishes its logo — see the spec's table")
	r.NoError(body.Close())

	// ...and clearing the asset, which IS the un-publish mechanism, still works
	// on a disabled page. Without this the loosening would leave an operator no
	// way to take the blob down.
	_, err = s.svc.Clear(t.Context(), "acme", "acme-status", statuspageassets.KindLogo)
	r.NoError(err)

	_, _, err = s.openPublic(t, uid)
	r.ErrorIs(err, files.ErrFileNotFound)
}

// TestPasswordPageStillServesItsAssets is the same recorded decision for a
// page shared behind a secret: the logo is the image shown ABOVE the password
// prompt, so serving it leaks nothing the prompt does not already show.
func TestPasswordPageStillServesItsAssets(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newAssetSetup(t)

	page, err := s.svc.Upload(t.Context(), "acme", "acme-status", statuspageassets.KindLogo, upload(pngBytes), nil)
	r.NoError(err)
	uid := logoUID(t, page)

	visibility := models.StatusPageVisibilityPassword
	hash := "$argon2id$fake"
	r.NoError(s.dbSvc.UpdateStatusPage(t.Context(), page.UID, &models.StatusPageUpdate{
		Visibility: &visibility, PasswordHash: &hash,
	}))

	_, body, err := s.openPublic(t, uid)
	r.NoError(err)
	r.NoError(body.Close())

	// The same for a fully private page.
	visibility = models.StatusPageVisibilityPrivate
	r.NoError(s.dbSvc.UpdateStatusPage(t.Context(), page.UID, &models.StatusPageUpdate{Visibility: &visibility}))

	_, body, err = s.openPublic(t, uid)
	r.NoError(err)
	r.NoError(body.Close())
}

// TestDeletedPageStopsServingItsAssets is the REAP assertion.
//
// Deleting a page must take its logo offline, and after this refactor nothing
// makes that happen implicitly — the page row disappearing is invisible to a
// topic-based check. It only works because statuspages.Service.DeleteStatusPage
// reaps `status-pages/<uid>/`, which is exactly why this test drives the real
// delete path instead of db.DeleteStatusPage.
func TestDeletedPageStopsServingItsAssets(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newAssetSetup(t)

	page, err := s.svc.Upload(t.Context(), "acme", "acme-status", statuspageassets.KindLogo, upload(pngBytes), nil)
	r.NoError(err)
	uid := logoUID(t, page)

	// Positive control: it resolves right up until the delete.
	_, body, err := s.openPublic(t, uid)
	r.NoError(err)
	r.NoError(body.Close())

	pagesSvc := statuspages.NewService(s.dbSvc, &config.Config{}, nil)
	r.NoError(pagesSvc.DeleteStatusPage(t.Context(), "acme", page.UID))

	_, _, err = s.openPublic(t, uid)
	r.ErrorIs(err, files.ErrFileNotFound, "deleting a page must reap its brand assets")
}

// TestSoftDeletingThePageRowAloneIsNotEnough is the negative control for the
// test above: it pins WHY the reap exists. Take the reap away and the page row
// vanishing leaves the blob world-readable.
func TestSoftDeletingThePageRowAloneIsNotEnough(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newAssetSetup(t)

	page, err := s.svc.Upload(t.Context(), "acme", "acme-status", statuspageassets.KindLogo, upload(pngBytes), nil)
	r.NoError(err)
	uid := logoUID(t, page)

	r.NoError(s.dbSvc.DeleteStatusPage(t.Context(), page.UID))

	_, body, err := s.openPublic(t, uid)
	r.NoError(err, "the page row is invisible to a topic check — only the reap un-publishes")
	r.NoError(body.Close())
}

// TestUploadRejectsOversizeAndEmpty pins the two size guards. The handler
// additionally bounds the raw body with MaxBytesReader; this covers the
// service half, which is what the MCP/other non-HTTP callers would hit.
func TestUploadRejectsOversizeAndEmpty(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newAssetSetup(t)

	_, err := s.svc.Upload(t.Context(), "acme", "acme-status", statuspageassets.KindLogo, statuspageassets.Upload{
		Name: "big.png", MIMEType: "image/png",
		Size: statuspageassets.MaxLogoSize + 1, Body: strings.NewReader("x"),
	}, nil)
	r.ErrorIs(err, statuspageassets.ErrAssetTooLarge)

	_, err = s.svc.Upload(t.Context(), "acme", "acme-status", statuspageassets.KindFavicon, statuspageassets.Upload{
		Name: "big.ico", MIMEType: "image/png",
		Size: statuspageassets.MaxFaviconSize + 1, Body: strings.NewReader("x"),
	}, nil)
	r.ErrorIs(err, statuspageassets.ErrAssetTooLarge, "the favicon cap is tighter than the logo cap")

	_, err = s.svc.Upload(t.Context(), "acme", "acme-status", statuspageassets.KindLogo, statuspageassets.Upload{
		Name: "empty.png", MIMEType: "image/png", Size: 0, Body: strings.NewReader(""),
	}, nil)
	r.ErrorIs(err, statuspageassets.ErrEmptyAsset)
}

// TestNormalizeMIME covers the allowlist, the parameter stripping and the
// per-kind difference (JPEG is a logo format, never a favicon; .ico is a
// favicon format, never a logo).
func TestNormalizeMIME(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		kind     statuspageassets.Kind
		declared string
		want     string
		wantErr  bool
	}{
		{"logo png", statuspageassets.KindLogo, "image/png", "image/png", false},
		{"logo jpeg", statuspageassets.KindLogo, "image/jpeg", "image/jpeg", false},
		{
			"logo svg with charset", statuspageassets.KindLogo,
			"image/svg+xml; charset=utf-8", "image/svg+xml", false,
		},
		{"logo uppercase", statuspageassets.KindLogo, "IMAGE/PNG", "image/png", false},
		{"logo rejects ico", statuspageassets.KindLogo, "image/x-icon", "", true},
		{"logo rejects html", statuspageassets.KindLogo, "text/html", "", true},
		{"logo rejects empty", statuspageassets.KindLogo, "  ", "", true},
		{"favicon ico", statuspageassets.KindFavicon, "image/x-icon", "image/x-icon", false},
		{
			"favicon ms ico", statuspageassets.KindFavicon,
			"image/vnd.microsoft.icon", "image/vnd.microsoft.icon", false,
		},
		{"favicon rejects jpeg", statuspageassets.KindFavicon, "image/jpeg", "", true},
		{"unknown kind", statuspageassets.Kind("banner"), "image/png", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			got, err := statuspageassets.NormalizeMIME(tc.kind, tc.declared)
			if tc.wantErr {
				r.Error(err)

				return
			}

			r.NoError(err)
			r.Equal(tc.want, got)
		})
	}
}

// TestPublicURLIsNilForAnEmptySlot: an absent asset must not produce a URL
// that would 404 on every page load.
func TestPublicURLIsNilForAnEmptySlot(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Nil(statuspageassets.PublicURL(nil))

	empty := ""
	r.Nil(statuspageassets.PublicURL(&empty))
}

// TestUnknownOrgOrPageIsNotFound keeps the two lookups from leaking as 500s.
func TestUnknownOrgOrPageIsNotFound(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newAssetSetup(t)

	_, err := s.svc.Upload(t.Context(), "nope", "acme-status", statuspageassets.KindLogo, upload(pngBytes), nil)
	r.ErrorIs(err, statuspageassets.ErrOrganizationNotFound)

	_, err = s.svc.Upload(t.Context(), "acme", "no-such-page", statuspageassets.KindLogo, upload(pngBytes), nil)
	r.ErrorIs(err, statuspageassets.ErrStatusPageNotFound)
}
