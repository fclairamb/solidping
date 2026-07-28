package postgres

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portStatusPageCustomCSS is distinct from every other _postgres_test.go
// embedded port in this package (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portStatusPageCustomCSS = 15465

// TestStatusPageCustomCSS_Postgres is the Postgres twin of the SQLite
// custom_css round-trip (spec 2026-07-27-02). It doubles as the consolidation
// regression check: the column now ships inside the consolidated 008_v0_7_0
// migration, so a fresh database must have it.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres pwfile extraction) with its siblings
func TestStatusPageCustomCSS_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{
		Embedded: true,
		Port:     portStatusPageCustomCSS,
		RunMode:  runModeTest,
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("css-pg", "CSS PG")
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
		{name: "set", write: strPtr(":root { --brand: #ff0000; }"), want: strPtr(":root { --brand: #ff0000; }")},
		{name: "untouched by nil", write: nil, want: strPtr(":root { --brand: #ff0000; }")},
		{name: "cleared by empty string", write: strPtr(""), want: nil},
		{
			name:  "large payload just under the API cap",
			write: strPtr(strings.Repeat("a", 64*1024)),
			want:  strPtr(strings.Repeat("a", 64*1024)),
		},
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

	// The by-slug lookup status0 uses must carry the value too.
	css := ":root { --brand: #123456; }"
	r.NoError(s.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{CustomCSS: &css}))
	bySlug, err := s.GetStatusPageBySlug(ctx, org.UID, page.Slug)
	r.NoError(err)
	r.NotNil(bySlug.CustomCSS)
	r.Equal(css, *bySlug.CustomCSS)
}

func strPtr(s string) *string { return &s }
