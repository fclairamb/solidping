package msteams

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"image/png"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildManifest_FillsInstanceValues(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	manifest := BuildManifest(testAppID, "https://monitor.example.com/")

	r.Equal(testAppID, manifest.ID)
	r.Equal(manifestVersion, manifest.ManifestVersion)
	r.Len(manifest.Bots, 1)
	r.Equal(testAppID, manifest.Bots[0].BotID)
	// Personal scope is explicitly phase 2 — declaring it would surface a
	// dead Chat tab in Teams.
	r.Equal([]string{"team", "groupChat"}, manifest.Bots[0].Scopes)
	r.NotEmpty(manifest.Bots[0].CommandLists)
	// The trailing slash must not leak into the developer URLs.
	r.Equal("https://monitor.example.com", manifest.Developer.WebsiteURL)
	// validDomains must hold a bare host, never a scheme.
	r.Equal([]string{"monitor.example.com"}, manifest.ValidDomains)
}

func TestBuildManifest_ValidDomainsIgnoresPortAndScheme(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	manifest := BuildManifest(testAppID, "https://monitor.example.com:8443")
	r.Equal([]string{"monitor.example.com"}, manifest.ValidDomains)
}

func TestBuildAppPackage_ContainsManifestAndIcons(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	pkg, err := BuildAppPackage(testAppID, "https://monitor.example.com")
	r.NoError(err)
	r.NotEmpty(pkg)

	reader, err := zip.NewReader(bytes.NewReader(pkg), int64(len(pkg)))
	r.NoError(err)

	files := make(map[string][]byte, len(reader.File))

	for _, file := range reader.File {
		rc, openErr := file.Open()
		r.NoError(openErr)

		data, readErr := io.ReadAll(rc)
		r.NoError(readErr)
		r.NoError(rc.Close())

		files[file.Name] = data
	}

	r.Contains(files, "manifest.json")
	r.Contains(files, "color.png")
	r.Contains(files, "outline.png")

	var decoded Manifest
	r.NoError(json.Unmarshal(files["manifest.json"], &decoded))
	r.Equal(testAppID, decoded.ID)

	// Both icons must be decodable PNGs, and the outline must be exactly
	// 32x32 — Teams rejects the package otherwise.
	colorImg, err := png.Decode(bytes.NewReader(files["color.png"]))
	r.NoError(err)
	r.Positive(colorImg.Bounds().Dx())

	outlineImg, err := png.Decode(bytes.NewReader(files["outline.png"]))
	r.NoError(err)
	r.Equal(outlineIconSize, outlineImg.Bounds().Dx())
	r.Equal(outlineIconSize, outlineImg.Bounds().Dy())
}
