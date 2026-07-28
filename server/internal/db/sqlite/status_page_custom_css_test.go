package sqlite

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestStatusPageCustomCSS_RoundTrip covers the whole custom_css lifecycle at
// the storage layer (spec 2026-07-27-02): created NULL, set on update, updated
// again, cleared by an empty string, and left untouched by a nil pointer.
func TestStatusPageCustomCSS_RoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s := newCustomDomainTestService(t)

	org := models.NewOrganization("css-org", "CSS Org")
	r.NoError(s.CreateOrganization(ctx, org))

	page := models.NewStatusPage(org.UID, "Acme", "main")
	r.NoError(s.CreateStatusPage(ctx, page))

	got, err := s.GetStatusPage(ctx, org.UID, page.UID)
	r.NoError(err)
	r.Nil(got.CustomCSS, "a fresh page has no custom CSS")

	tests := []struct {
		name  string
		write *string
		want  *string
	}{
		{name: "set", write: ptr(":root { --brand: #ff0000; }"), want: ptr(":root { --brand: #ff0000; }")},
		{name: "replace", write: ptr(".dark { --background: #000; }"), want: ptr(".dark { --background: #000; }")},
		{name: "untouched by nil", write: nil, want: ptr(".dark { --background: #000; }")},
		{name: "cleared by empty string", write: ptr(""), want: nil},
		{name: "large payload just under the API cap", write: ptr(strings.Repeat("a", 64*1024)), want: ptr(strings.Repeat("a", 64*1024))},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rr := require.New(t)
			rr.NoError(s.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{CustomCSS: tc.write}))

			after, getErr := s.GetStatusPage(ctx, org.UID, page.UID)
			rr.NoError(getErr)

			if tc.want == nil {
				rr.Nil(after.CustomCSS)

				return
			}

			rr.NotNil(after.CustomCSS)
			rr.Equal(*tc.want, *after.CustomCSS)
		})
	}
}

// TestStatusPageCustomCSS_CreateCarriesValue proves the column is written by a
// plain insert too (the create path sets the model field directly).
func TestStatusPageCustomCSS_CreateCarriesValue(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s := newCustomDomainTestService(t)

	org := models.NewOrganization("css-org2", "CSS Org 2")
	r.NoError(s.CreateOrganization(ctx, org))

	css := ":root { --brand: #00ff00; }"
	page := models.NewStatusPage(org.UID, "Acme", "main")
	page.CustomCSS = &css
	r.NoError(s.CreateStatusPage(ctx, page))

	got, err := s.GetStatusPage(ctx, org.UID, page.UID)
	r.NoError(err)
	r.NotNil(got.CustomCSS)
	r.Equal(css, *got.CustomCSS)

	// The public-by-slug lookup — the one status0 actually calls — must carry
	// the CSS too, otherwise the page renders unstyled.
	bySlug, err := s.GetStatusPageBySlug(ctx, org.UID, page.Slug)
	r.NoError(err)
	r.NotNil(bySlug.CustomCSS)
	r.Equal(css, *bySlug.CustomCSS)
}

func ptr(s string) *string { return &s }
