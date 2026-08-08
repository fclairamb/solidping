package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

// widgetSourcePath is the widget's TypeScript source, relative to this
// package. The Go test guards it because the file is the one piece of
// SolidPing code that executes on THIRD-PARTY pages: a regression there is an
// XSS on a customer's site, not on ours.
const widgetSourcePath = "../../../web/status0/src/embed/widget.ts"

// newEmbedWidgetTestFS stands in for the real (gitignored) status0 build.
func newEmbedWidgetTestFS(body string) fstest.MapFS {
	return fstest.MapFS{
		embedWidgetV1Path: &fstest.MapFile{Data: []byte(body)},
	}
}

func TestServeEmbedWidgetV1(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	server := &Server{status0FS: newEmbedWidgetTestFS("(()=>{/*widget*/})();")}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/embed/v1/widget.js", nil)

	r.NoError(server.serveEmbedWidgetV1(recorder, req))
	r.Equal(http.StatusOK, recorder.Code)
	// Content type must be JavaScript: a customer's <script> tag is refused
	// under nosniff if we ever serve it as text/plain.
	r.Equal("application/javascript; charset=utf-8", recorder.Header().Get("Content-Type"))
	// Long, public cache: the script is immutable within /embed/v1/, unlike
	// the 60 s summary data it polls.
	r.Equal("public, max-age=3600", recorder.Header().Get("Cache-Control"))
	r.Equal("(()=>{/*widget*/})();", recorder.Body.String())
}

// TestServeEmbedWidgetV1Missing covers a binary built without a status0 widget
// asset: it must 404 rather than serve an empty body that a customer's page
// would silently execute as a no-op script.
func TestServeEmbedWidgetV1Missing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	server := &Server{status0FS: fstest.MapFS{}}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/embed/v1/widget.js", nil)

	r.NoError(server.serveEmbedWidgetV1(recorder, req))
	r.Equal(http.StatusNotFound, recorder.Code)
}

// TestEmbeddedStatus0ContainsEmbedWidget asserts the widget really rides along
// in the embedded status0 filesystem — i.e. that web/status0's `build:widget`
// step and the copy-status0 Makefile target actually put the file where the
// route reads it. Without this, a build-pipeline regression would only surface
// as a 404 on customers' live sites.
func TestEmbeddedStatus0ContainsEmbedWidget(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	data, err := status0Files.ReadFile(embedWidgetV1Path)
	r.NoError(err, "the status0 build must emit %s (see web/status0 build:widget)", embedWidgetV1Path)
	r.NotEmpty(data)
}

// TestEmbedWidgetSourceIsXSSSafe is a static guard on the widget source. The
// widget writes values it does not control into the DOM (per-state labels from
// host-page data-attributes, the status page name, and the link target from
// the summary API), on pages that belong to customers. Two invariants keep
// that safe, and both are cheap to break by accident:
//
//   - no HTML-parsing DOM sink is used anywhere (innerHTML / outerHTML /
//     insertAdjacentHTML / document.write) — text goes in via textContent;
//   - the link target is scheme-checked before becoming an href, so a hostile
//     `page.url` of `javascript:...` can never be clicked into execution.
//
// The behavioral counterpart (a real browser, real hostile values) lives in
// web/status0/e2e/embed-widget.spec.ts.
func TestEmbedWidgetSourceIsXSSSafe(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	raw, err := os.ReadFile(filepath.Clean(widgetSourcePath))
	r.NoError(err, "widget source must be readable at %s", widgetSourcePath)

	source := string(raw)

	// Strip the leading block comment: it describes the forbidden sinks by
	// name, which would otherwise trip the scan below.
	if end := strings.Index(source, "*/"); end != -1 {
		source = source[end+2:]
	}

	for _, sink := range []string{
		".innerHTML",
		".outerHTML",
		"insertAdjacentHTML",
		"document.write",
	} {
		r.NotContains(source, sink,
			"the embed widget runs on third-party pages and must never use %s", sink)
	}

	r.Contains(source, "textContent", "labels must be written via textContent")
	r.Contains(source, `parsed.protocol !== "http:"`,
		"the link target must be scheme-validated before it becomes an href")
	r.Contains(source, `credentials: "omit"`,
		"the summary fetch must never carry credentials")
	r.Contains(source, "encodeURIComponent",
		"data-page segments must be URL-encoded into the request path")
}
