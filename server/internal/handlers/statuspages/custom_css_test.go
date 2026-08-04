package statuspages

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestValidateCustomCSS is the table-driven contract for the two write-side
// rules (spec 2026-07-27-02): a 64 KB cap and a case-insensitive, position-
// independent @import rejection. External url() must stay allowed.
func TestValidateCustomCSS(t *testing.T) {
	t.Parallel()

	css := func(s string) *string { return &s }

	tests := []struct {
		name    string
		input   *string
		wantErr error
	}{
		{name: "omitted field is valid", input: nil},
		{name: "empty string is valid (clears the field)", input: css("")},
		{name: "plain theme override", input: css(":root { --brand: #ff5500; }")},
		{name: "dark variant", input: css(".dark { --background: #111; --foreground: #eee; }")},
		{
			name:  "external url() is deliberately allowed",
			input: css("@font-face { font-family: X; src: url(https://fonts.example/x.woff2); }"),
		},
		{
			name:  "background image url() is allowed",
			input: css("body { background-image: url('https://cdn.example/bg.png'); }"),
		},
		{name: "exactly at the cap", input: css(strings.Repeat("a", MaxCustomCSSBytes))},
		{name: "one byte over the cap", input: css(strings.Repeat("a", MaxCustomCSSBytes+1)), wantErr: ErrCustomCSSTooLarge},
		{name: "lowercase @import", input: css("@import url('https://evil.example/x.css');"), wantErr: ErrCustomCSSImport},
		{name: "uppercase @IMPORT", input: css("@IMPORT url('x.css');"), wantErr: ErrCustomCSSImport},
		{name: "mixed-case @ImPoRt", input: css("@ImPoRt 'x.css';"), wantErr: ErrCustomCSSImport},
		{
			name:    "@import buried mid-file",
			input:   css(":root { --brand: red; }\n\n  @import url(x.css);\n.dark { }"),
			wantErr: ErrCustomCSSImport,
		},
		{
			name:    "@import hidden in a comment",
			input:   css("/* @import url(x.css); */ :root { --brand: red; }"),
			wantErr: ErrCustomCSSImport,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateCustomCSS(tc.input)
			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

// TestCreateStatusPage_CustomCSS covers the create path: the value round-trips
// onto the response, an empty string stays NULL, and a rejected payload never
// reaches the database.
func TestCreateStatusPage_CustomCSS(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	css := ":root { --brand: #ff5500; }"
	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Public", Slug: testPublicSlug, CustomCSS: &css,
	})
	r.NoError(err)
	r.NotNil(page.CustomCSS)
	r.Equal(css, *page.CustomCSS)

	empty := ""
	page2, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Second", Slug: "second", CustomCSS: &empty,
	})
	r.NoError(err)
	r.Nil(page2.CustomCSS, "an empty stylesheet stays NULL")

	bad := "@import url('https://evil.example/x.css');"
	_, err = svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Third", Slug: "third", CustomCSS: &bad,
	})
	r.ErrorIs(err, ErrCustomCSSImport)

	oversize := strings.Repeat("a", MaxCustomCSSBytes+1)
	_, err = svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Fourth", Slug: "fourth", CustomCSS: &oversize,
	})
	r.ErrorIs(err, ErrCustomCSSTooLarge)
}

// TestUpdateStatusPage_CustomCSS covers set / replace / leave-alone / clear and
// the two rejections on the update path.
func TestUpdateStatusPage_CustomCSS(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	created, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Public", Slug: testPublicSlug,
	})
	r.NoError(err)
	r.Nil(created.CustomCSS)

	set := ":root { --brand: #123456; }"
	updated, err := svc.UpdateStatusPage(ctx, org.Slug, created.UID, &UpdateStatusPageRequest{CustomCSS: &set})
	r.NoError(err)
	r.Equal(set, *updated.CustomCSS)

	// An unrelated update must not disturb the stylesheet.
	newName := "Renamed"
	updated, err = svc.UpdateStatusPage(ctx, org.Slug, created.UID, &UpdateStatusPageRequest{Name: &newName})
	r.NoError(err)
	r.NotNil(updated.CustomCSS)
	r.Equal(set, *updated.CustomCSS)

	// Rejections leave the stored value untouched.
	bad := "body { }\n@IMPORT url(x.css);"
	_, err = svc.UpdateStatusPage(ctx, org.Slug, created.UID, &UpdateStatusPageRequest{CustomCSS: &bad})
	r.ErrorIs(err, ErrCustomCSSImport)

	oversize := strings.Repeat("b", MaxCustomCSSBytes+1)
	_, err = svc.UpdateStatusPage(ctx, org.Slug, created.UID, &UpdateStatusPageRequest{CustomCSS: &oversize})
	r.ErrorIs(err, ErrCustomCSSTooLarge)

	stillThere, err := svc.GetStatusPage(ctx, org.Slug, created.UID, GetStatusPageOptions{})
	r.NoError(err)
	r.Equal(set, *stillThere.CustomCSS)

	// An empty string clears it.
	empty := ""
	cleared, err := svc.UpdateStatusPage(ctx, org.Slug, created.UID, &UpdateStatusPageRequest{CustomCSS: &empty})
	r.NoError(err)
	r.Nil(cleared.CustomCSS)
}

// TestViewStatusPage_CustomCSSIsPublic pins the deliberate asymmetry with the
// custom-domain fields: customCss MUST reach the public responses, because
// status0 (the public renderer) is its only consumer.
func TestViewStatusPage_CustomCSSIsPublic(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	css := ":root { --brand: #00aa88; }"
	created, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Public", Slug: testPublicSlug, CustomCSS: &css,
	})
	r.NoError(err)
	r.True(created.IsDefault, "the first page of an org becomes the default")

	public, err := svc.ViewStatusPage(ctx, org.Slug, testPublicSlug)
	r.NoError(err)
	r.NotNil(public.CustomCSS, "the public view must carry the stylesheet")
	r.Equal(css, *public.CustomCSS)
	r.Nil(public.CustomDomain, "the custom-domain fields stay auth-only")

	byDefault, err := svc.ViewDefaultStatusPage(ctx, org.Slug)
	r.NoError(err)
	r.NotNil(byDefault.CustomCSS)
	r.Equal(css, *byDefault.CustomCSS)
}
