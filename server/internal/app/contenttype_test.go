package app

import (
	"context"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The bug this guards: mime.TypeByExtension takes an extension, and every
// caller here has a whole embed path. Passing the path returns "" for files Go
// knows perfectly well.
func TestContentTypeForFileTakesAPathNotAnExtension(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Positive control: Go really does know .html, so an empty answer for the
	// path below can only be the path/extension mix-up and nothing else.
	r.NotEmpty(mime.TypeByExtension(".html"))
	r.Empty(mime.TypeByExtension("openapi/index.html"),
		"if this ever resolves, the bug being guarded has changed shape")

	r.True(strings.HasPrefix(contentTypeForFile("openapi/index.html"), "text/html"))
	r.True(strings.HasPrefix(contentTypeForFile("index.html"), "text/html"))
}

// .yaml resolves from the OS mime database on some machines and not others, so
// it is pinned rather than left to vary between a laptop and CI.
func TestContentTypeForFileResolvesYAMLEverywhere(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("application/yaml; charset=utf-8", contentTypeForFile("openapi/openapi.yaml"))
	r.Equal("application/yaml; charset=utf-8", contentTypeForFile("x.YML"))
}

// The pin must be consulted BEFORE Go's table, not as a fallback after it.
//
// This is the case that shipped broken: mime.TypeByExtension(".yaml") answers
// "application/yaml" on a typical Linux image and "" on macOS, so a pin applied
// only when the OS said nothing produced a Content-Type that differed between a
// developer laptop and CI — while passing the suite on both. Asserting equality
// against whatever the host reports is what makes this fail on EITHER platform
// if the precedence is ever flipped back.
//
// It mutates the process-global mime table. That is safe to run in parallel
// here: the mime package guards the table itself, the call is idempotent, and
// no other test asserts anything about .yaml's raw entry — contentTypeForFile
// short-circuits it on the pin either way.
func TestContentTypeForFilePinsAheadOfTheHostMimeTable(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Reproduce the CI condition on ANY host rather than depending on the one
	// this happens to run on: teach Go's table the same answer a Linux image
	// gives. A macOS box reports "" for .yaml, so without this the regression
	// is simply invisible here — which is exactly how it shipped.
	r.NoError(mime.AddExtensionType(".yaml", "application/yaml"))
	r.Equal("application/yaml", mime.TypeByExtension(".yaml"), "precondition")

	r.Equal("application/yaml; charset=utf-8", contentTypeForFile("openapi/openapi.yaml"),
		"the pin must win over the host mime table, not merely fill in for it")
}

// "" means "let net/http sniff". It must never be turned into a guess, and the
// caller must never write it through as a header value.
func TestContentTypeForFileSaysNothingRatherThanGuessing(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Empty(contentTypeForFile("some/blob"))
	r.Empty(contentTypeForFile("archive.weird-extension"))
}

// The regression test the shared helper never had: a file served through
// serveFile must go out with a real Content-Type, not an empty header.
func TestServeFileSetsARealContentType(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server := &Server{}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/openapi", nil)

	r.NoError(server.serveFile(openAPIFiles, "openapi/index.html")(recorder, req))

	r.Equal(http.StatusOK, recorder.Code)

	contentType := recorder.Header().Get("Content-Type")
	r.NotEmpty(contentType, "an empty Content-Type also suppresses net/http sniffing")
	r.True(strings.HasPrefix(contentType, "text/html"), "got %q", contentType)
	r.Contains(recorder.Body.String(), "api-reference")
}
