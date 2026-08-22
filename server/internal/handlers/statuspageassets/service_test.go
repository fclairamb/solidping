package statuspageassets_test

import (
	"context"
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
)

// pngBytes is a one-pixel PNG. Content is irrelevant to this package (it never
// decodes the image), but a plausible blob keeps the fixtures honest.
const pngBytes = "\x89PNG\r\n\x1a\n-not-really-a-png-but-opaque-bytes"

type assetSetup struct {
	ctx   context.Context
	dbSvc db.Service
	svc   *statuspageassets.Service
	org   *models.Organization
	page  *models.StatusPage
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

	return &assetSetup{
		ctx:   ctx,
		dbSvc: dbSvc,
		svc:   statuspageassets.NewService(dbSvc, files.NewService(dbSvc, cfg)),
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
// the column gets the file UID, the public URL is derived from it, and the
// unsigned public route resolves the blob back.
func TestUploadPointsThePageAtTheFileAndServesIt(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newAssetSetup(t)

	page, err := s.svc.Upload(s.ctx, "acme", "acme-status", statuspageassets.KindLogo, upload(pngBytes), nil)
	r.NoError(err)
	r.NotNil(page.LogoFileUID)
	r.Nil(page.FaviconFileUID, "uploading a logo must not touch the favicon slot")

	url := statuspageassets.PublicURL(page.LogoFileUID)
	r.NotNil(url)
	r.Equal(statuspageassets.PublicPathPrefix+*page.LogoFileUID, *url)

	file, body, err := s.svc.OpenAsset(s.ctx, *page.LogoFileUID)
	r.NoError(err)

	defer func() { _ = body.Close() }()

	r.Equal("image/png", file.MimeType)

	got, err := io.ReadAll(body)
	r.NoError(err)
	r.Equal(pngBytes, string(got))
}

// TestFaviconSlotIsIndependent proves the two columns really are separate
// slots: writing one leaves the other alone, in both directions.
func TestFaviconSlotIsIndependent(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newAssetSetup(t)

	_, err := s.svc.Upload(s.ctx, "acme", "acme-status", statuspageassets.KindLogo, upload(pngBytes), nil)
	r.NoError(err)

	page, err := s.svc.Upload(s.ctx, "acme", "acme-status", statuspageassets.KindFavicon, upload("icon"), nil)
	r.NoError(err)
	r.NotNil(page.LogoFileUID)
	r.NotNil(page.FaviconFileUID)
	r.NotEqual(*page.LogoFileUID, *page.FaviconFileUID)

	page, err = s.svc.Clear(s.ctx, "acme", "acme-status", statuspageassets.KindFavicon)
	r.NoError(err)
	r.NotNil(page.LogoFileUID, "clearing the favicon must not clear the logo")
	r.Nil(page.FaviconFileUID)
}

// TestReplacingAnAssetUnpublishesTheOldBlob is the security property of the
// unsigned public route: authorization is state, so the moment the page stops
// pointing at a file, that file stops being served.
func TestReplacingAnAssetUnpublishesTheOldBlob(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newAssetSetup(t)

	first, err := s.svc.Upload(s.ctx, "acme", "acme-status", statuspageassets.KindLogo, upload("one"), nil)
	r.NoError(err)
	oldUID := *first.LogoFileUID

	second, err := s.svc.Upload(s.ctx, "acme", "acme-status", statuspageassets.KindLogo, upload("two"), nil)
	r.NoError(err)
	r.NotEqual(oldUID, *second.LogoFileUID)

	_, _, err = s.svc.OpenAsset(s.ctx, oldUID)
	r.ErrorIs(err, statuspageassets.ErrAssetNotFound, "the replaced blob must stop resolving")

	_, body, err := s.svc.OpenAsset(s.ctx, *second.LogoFileUID)
	r.NoError(err)
	r.NoError(body.Close())
}

// TestClearingAnAssetUnpublishesTheBlob is the same property for an explicit
// removal with no replacement.
func TestClearingAnAssetUnpublishesTheBlob(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newAssetSetup(t)

	page, err := s.svc.Upload(s.ctx, "acme", "acme-status", statuspageassets.KindLogo, upload(pngBytes), nil)
	r.NoError(err)
	uid := *page.LogoFileUID

	cleared, err := s.svc.Clear(s.ctx, "acme", "acme-status", statuspageassets.KindLogo)
	r.NoError(err)
	r.Nil(cleared.LogoFileUID)

	_, _, err = s.svc.OpenAsset(s.ctx, uid)
	r.ErrorIs(err, statuspageassets.ErrAssetNotFound)
}

// TestDisabledPageStopsServingItsAssets: turning a page off must take its
// brand assets offline with it, exactly as it takes the page offline. This is
// the negative control the "current asset of a LIVE, ENABLED page" rule exists
// for — without the enabled check the blob would still be world-readable.
func TestDisabledPageStopsServingItsAssets(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newAssetSetup(t)

	page, err := s.svc.Upload(s.ctx, "acme", "acme-status", statuspageassets.KindLogo, upload(pngBytes), nil)
	r.NoError(err)
	uid := *page.LogoFileUID

	// Positive control first: it resolves while the page is enabled.
	_, body, err := s.svc.OpenAsset(s.ctx, uid)
	r.NoError(err)
	r.NoError(body.Close())

	disabled := false
	r.NoError(s.dbSvc.UpdateStatusPage(s.ctx, page.UID, &models.StatusPageUpdate{Enabled: &disabled}))

	_, _, err = s.svc.OpenAsset(s.ctx, uid)
	r.ErrorIs(err, statuspageassets.ErrAssetNotFound)
}

// TestDeletedPageStopsServingItsAssets is the same for a soft-deleted page.
func TestDeletedPageStopsServingItsAssets(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newAssetSetup(t)

	page, err := s.svc.Upload(s.ctx, "acme", "acme-status", statuspageassets.KindLogo, upload(pngBytes), nil)
	r.NoError(err)
	uid := *page.LogoFileUID

	r.NoError(s.dbSvc.DeleteStatusPage(s.ctx, page.UID))

	_, _, err = s.svc.OpenAsset(s.ctx, uid)
	r.ErrorIs(err, statuspageassets.ErrAssetNotFound)
}

// TestUploadRejectsOversizeAndEmpty pins the two size guards. The handler
// additionally bounds the raw body with MaxBytesReader; this covers the
// service half, which is what the MCP/other non-HTTP callers would hit.
func TestUploadRejectsOversizeAndEmpty(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newAssetSetup(t)

	_, err := s.svc.Upload(s.ctx, "acme", "acme-status", statuspageassets.KindLogo, statuspageassets.Upload{
		Name: "big.png", MIMEType: "image/png",
		Size: statuspageassets.MaxLogoSize + 1, Body: strings.NewReader("x"),
	}, nil)
	r.ErrorIs(err, statuspageassets.ErrAssetTooLarge)

	_, err = s.svc.Upload(s.ctx, "acme", "acme-status", statuspageassets.KindFavicon, statuspageassets.Upload{
		Name: "big.ico", MIMEType: "image/png",
		Size: statuspageassets.MaxFaviconSize + 1, Body: strings.NewReader("x"),
	}, nil)
	r.ErrorIs(err, statuspageassets.ErrAssetTooLarge, "the favicon cap is tighter than the logo cap")

	_, err = s.svc.Upload(s.ctx, "acme", "acme-status", statuspageassets.KindLogo, statuspageassets.Upload{
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
		{"logo svg with charset", statuspageassets.KindLogo,
			"image/svg+xml; charset=utf-8", "image/svg+xml", false},
		{"logo uppercase", statuspageassets.KindLogo, "IMAGE/PNG", "image/png", false},
		{"logo rejects ico", statuspageassets.KindLogo, "image/x-icon", "", true},
		{"logo rejects html", statuspageassets.KindLogo, "text/html", "", true},
		{"logo rejects empty", statuspageassets.KindLogo, "  ", "", true},
		{"favicon ico", statuspageassets.KindFavicon, "image/x-icon", "image/x-icon", false},
		{"favicon ms ico", statuspageassets.KindFavicon,
			"image/vnd.microsoft.icon", "image/vnd.microsoft.icon", false},
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

	_, err := s.svc.Upload(s.ctx, "nope", "acme-status", statuspageassets.KindLogo, upload(pngBytes), nil)
	r.ErrorIs(err, statuspageassets.ErrOrganizationNotFound)

	_, err = s.svc.Upload(s.ctx, "acme", "no-such-page", statuspageassets.KindLogo, upload(pngBytes), nil)
	r.ErrorIs(err, statuspageassets.ErrStatusPageNotFound)
}
