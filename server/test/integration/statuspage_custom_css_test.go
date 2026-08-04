package integration

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// customCSSPageResponse is the slice of the status-page payload this test cares
// about; the shared statusPageResponse type deliberately stays minimal.
type customCSSPageResponse struct {
	UID       string  `json:"uid"`
	Slug      string  `json:"slug"`
	CustomCSS *string `json:"customCss,omitempty"`
}

type apiErrorResponse struct {
	Title  string `json:"title"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// TestStatusPageCustomCSS_HTTPLifecycle drives customCss end-to-end over HTTP
// (spec 2026-07-27-02): create with it, read it back, clear it, and — the
// property that matters most — see it on the UNAUTHENTICATED public view,
// which is what status0 renders from.
func TestStatusPageCustomCSS_HTTPLifecycle(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newStatusPageTestHelper(t)

	css := ":root { --brand: #ff5500; }"

	created, status := doJSON[customCSSPageResponse](t, h, "POST",
		"/api/v1/orgs/"+TestOrgSlug+"/status-pages", map[string]any{
			"name": "Themed", "slug": "themed-page", "visibility": "public", "customCss": css,
		})
	r.Equal(http.StatusCreated, status)
	r.NotNil(created.CustomCSS)
	r.Equal(css, *created.CustomCSS)

	fetched, status := doJSON[customCSSPageResponse](t, h, "GET",
		"/api/v1/orgs/"+TestOrgSlug+"/status-pages/"+created.UID, nil)
	r.Equal(http.StatusOK, status)
	r.Equal(css, *fetched.CustomCSS)

	// The public view is unauthenticated and MUST carry the stylesheet.
	public, status := doGetPublic[customCSSPageResponse](t, h,
		"/api/v1/status-pages/"+TestOrgSlug+"/themed-page")
	r.Equal(http.StatusOK, status)
	r.NotNil(public.CustomCSS, "the public status-page response must include customCss")
	r.Equal(css, *public.CustomCSS)

	// PATCH replaces it.
	replaced := ".dark { --background: #0a0a0a; }"
	updated, status := doJSON[customCSSPageResponse](t, h, "PATCH",
		"/api/v1/orgs/"+TestOrgSlug+"/status-pages/"+created.UID,
		map[string]any{"customCss": replaced})
	r.Equal(http.StatusOK, status)
	r.Equal(replaced, *updated.CustomCSS)

	// An unrelated PATCH leaves it alone.
	untouched, status := doJSON[customCSSPageResponse](t, h, "PATCH",
		"/api/v1/orgs/"+TestOrgSlug+"/status-pages/"+created.UID,
		map[string]any{"name": "Renamed"})
	r.Equal(http.StatusOK, status)
	r.NotNil(untouched.CustomCSS)
	r.Equal(replaced, *untouched.CustomCSS)

	// An empty string clears it — the appearance editor's "empty textarea".
	cleared, status := doJSON[customCSSPageResponse](t, h, "PATCH",
		"/api/v1/orgs/"+TestOrgSlug+"/status-pages/"+created.UID,
		map[string]any{"customCss": ""})
	r.Equal(http.StatusOK, status)
	r.Nil(cleared.CustomCSS, "an empty customCss clears the field")
}

// TestStatusPageCustomCSS_Rejections pins the VALIDATION_ERROR contract for the
// two write-side rules, on both create and update.
func TestStatusPageCustomCSS_Rejections(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newStatusPageTestHelper(t)

	page, status := doJSON[customCSSPageResponse](t, h, "POST",
		"/api/v1/orgs/"+TestOrgSlug+"/status-pages", map[string]any{
			"name": "Reject", "slug": "reject-page",
		})
	r.Equal(http.StatusCreated, status)

	tests := []struct {
		name string
		slug string
		css  string
	}{
		{name: "import lowercase", slug: "css-import-lower", css: "@import url('https://evil.example/x.css');"},
		{name: "import uppercase", slug: "css-import-upper", css: "body{}\n@IMPORT 'x.css';"},
		{name: "oversize", slug: "css-oversize", css: strings.Repeat("a", 64*1024+1)},
	}

	for _, tc := range tests {
		t.Run(tc.name+" on create", func(t *testing.T) {
			t.Parallel()

			rr := require.New(t)
			errBody, st := doJSON[apiErrorResponse](t, h, "POST",
				"/api/v1/orgs/"+TestOrgSlug+"/status-pages",
				map[string]any{"name": "X", "slug": tc.slug, "customCss": tc.css})
			rr.Equal(http.StatusUnprocessableEntity, st)
			rr.Equal("VALIDATION_ERROR", errBody.Code)
		})

		t.Run(tc.name+" on update", func(t *testing.T) {
			t.Parallel()

			rr := require.New(t)
			errBody, st := doJSON[apiErrorResponse](t, h, "PATCH",
				"/api/v1/orgs/"+TestOrgSlug+"/status-pages/"+page.UID,
				map[string]any{"customCss": tc.css})
			rr.Equal(http.StatusUnprocessableEntity, st)
			rr.Equal("VALIDATION_ERROR", errBody.Code)
		})
	}

	// External url() must survive — blocking it would make the feature useless.
	ok, status := doJSON[customCSSPageResponse](t, h, "PATCH",
		"/api/v1/orgs/"+TestOrgSlug+"/status-pages/"+page.UID,
		map[string]any{"customCss": "body { background: url(https://cdn.example/bg.png); }"})
	r.Equal(http.StatusOK, status)
	r.NotNil(ok.CustomCSS)
}
