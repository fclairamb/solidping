package app

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

// assetFS is a small in-memory stand-in for an embedded build output.
func assetFS() fs.FS {
	return fstest.MapFS{
		"docsres/index.html":          {Data: []byte("<html><body>docs index</body></html>")},
		"docsres/assets/app.abc.js":   {Data: []byte("console.log('hi')")},
		"docsres/llms.txt":            {Data: []byte("plain text")},
		"docsres/binary.bin":          {Data: []byte{0x00, 0x01, 0x02, 0x03}},
		"docsres/nested/index.html":   {Data: []byte("<html>nested</html>")},
		"docsres/big.js":              {Data: []byte(strings.Repeat("x", 200_000))},
		"docsres/assets/style.a1.css": {Data: []byte("body{}")},
	}
}

func TestWriteEmbeddedFileSetsTypeAndLength(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	rec := httptest.NewRecorder()

	r.NoError(writeEmbeddedFile(rec, assetFS(), "docsres/index.html", "text/html", http.StatusOK))

	res := rec.Result()
	defer func() { _ = res.Body.Close() }()

	r.Equal(http.StatusOK, res.StatusCode)
	r.Equal("text/html", res.Header.Get("Content-Type"))
	r.Equal(strconv.Itoa(len("<html><body>docs index</body></html>")), res.Header.Get("Content-Length"))
	r.Equal("<html><body>docs index</body></html>", rec.Body.String())
}

// TestWriteEmbeddedFileSniffs pins that an unknown extension still gets a
// sniffed content type — identical to what DetectContentType produced on the
// full buffer before, since the sniffer never looks past 512 bytes anyway.
func TestWriteEmbeddedFileSniffs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	rec := httptest.NewRecorder()

	r.NoError(writeEmbeddedFile(rec, assetFS(), "docsres/binary.bin", "", http.StatusOK))

	r.Equal(http.DetectContentType([]byte{0x00, 0x01, 0x02, 0x03}), rec.Header().Get("Content-Type"))
	r.Equal([]byte{0x00, 0x01, 0x02, 0x03}, rec.Body.Bytes())
}

// TestWriteEmbeddedFileLargeBodyIntact is the correctness guard for streaming:
// a file larger than both the 512-byte sniff window and io.Copy's 32 KB buffer
// must arrive whole and unduplicated. An off-by-one in the sniff hand-off would
// show up here as a corrupted or repeated prefix.
func TestWriteEmbeddedFileLargeBodyIntact(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	rec := httptest.NewRecorder()

	// Sniffed (empty content type) so the head/tail hand-off is exercised.
	r.NoError(writeEmbeddedFile(rec, assetFS(), "docsres/big.js", "", http.StatusOK))

	r.Len(rec.Body.Bytes(), 200_000)
	r.Equal(strings.Repeat("x", 200_000), rec.Body.String())
	r.Equal("200000", rec.Header().Get("Content-Length"))
}

func TestWriteEmbeddedFileMissingAndDirectory(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	rec := httptest.NewRecorder()
	r.Error(writeEmbeddedFile(rec, assetFS(), "docsres/nope.html", "text/html", http.StatusOK))
	// A miss must not have written a status line, so the caller can still fall
	// through to the next candidate or its own 404.
	r.Empty(rec.Body.String())

	rec = httptest.NewRecorder()
	r.ErrorIs(writeEmbeddedFile(rec, assetFS(), "docsres/assets", "", http.StatusOK), errEmbeddedIsDirectory)
}

// TestServeDocsFileResolutionAndHeaders pins the docs handler's contract across
// the switch to streaming: candidate resolution order, the content types, and
// the cache policy that keeps HTML fresh while hashed assets cache for a year.
func TestServeDocsFileResolutionAndHeaders(t *testing.T) {
	t.Parallel()

	srv := &Server{}

	tests := []struct {
		name       string
		urlPath    string
		wantStatus int
		wantType   string
		wantMaxAge string
	}{
		{
			name: "root resolves to index.html", urlPath: "/",
			wantStatus: http.StatusOK, wantType: "text/html", wantMaxAge: "public, max-age=60",
		},
		{
			name: "exact asset path", urlPath: "/llms.txt",
			wantStatus: http.StatusOK, wantType: "text/plain", wantMaxAge: "public, max-age=60",
		},
		{
			name: "unknown path falls back to the 404 page", urlPath: "/definitely/not/a/page",
			wantStatus: http.StatusNotFound, wantType: "text/html", wantMaxAge: "public, max-age=60",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			rec := httptest.NewRecorder()
			srv.serveDocsFile(rec, testCase.urlPath)

			res := rec.Result()
			defer func() { _ = res.Body.Close() }()

			r.Equal(testCase.wantStatus, res.StatusCode)
			r.Contains(res.Header.Get("Content-Type"), testCase.wantType)
			r.Equal(testCase.wantMaxAge, res.Header.Get("Cache-Control"))
			r.NotEmpty(rec.Body.Bytes(), "a served docs file must have a body")
			// Streaming must still declare a length, so clients keep their
			// progress and the response stays non-chunked.
			r.Equal(strconv.Itoa(rec.Body.Len()), res.Header.Get("Content-Length"))
		})
	}
}

// TestDocsHashedAssetKeepsYearLongCache guards the half of the cache policy the
// table above cannot reach without knowing a hashed filename: anything that is
// not HTML/txt/xml caches for a year.
func TestDocsHashedAssetKeepsYearLongCache(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	entries, err := fs.ReadDir(docsFiles, "docsres/assets/js")
	if err != nil {
		t.Skip("no hashed docs assets embedded in this build")
	}

	var jsFile string

	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".js") {
			jsFile = entry.Name()

			break
		}
	}

	if jsFile == "" {
		t.Skip("no hashed js asset embedded in this build")
	}

	rec := httptest.NewRecorder()
	(&Server{}).serveDocsFile(rec, "/assets/js/"+jsFile)

	r.Equal(http.StatusOK, rec.Code)
	r.Equal("public, max-age=31536000", rec.Header().Get("Cache-Control"))
	r.Contains(rec.Header().Get("Content-Type"), "javascript")
}

func TestStaticContentType(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cases := map[string]string{
		"a/b/app.css":  contentTypeCSS,
		"a/b/app.js":   contentTypeJS,
		"a/logo.svg":   contentTypeSVG,
		"index.html":   contentTypeHTML,
		"icon.png":     contentTypePNG,
		"favicon.ico":  contentTypeICO,
		"noextension":  "",
		"data.unknown": "",
	}
	for path, want := range cases {
		r.Equal(want, staticContentType(path), path)
	}
}
