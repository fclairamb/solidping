package statuspages

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGenerateSlugAlwaysValid is the load-bearing test of this file: it pins
// that generateSlug is TOTAL over its input. Every one of these names used to
// be a way to put a string the database would reject into an INSERT, which
// surfaced as a 500 carrying a raw SQLSTATE rather than a validation error.
//
// The assertion is deliberately against slugRegex itself rather than against
// hand-written expectations, because slugRegex is what the
// `status_pages_slug_check` / `status_page_sections_slug_check` constraints
// mirror. If the regex and the constraints are ever widened together, this
// test keeps testing the right property.
func TestGenerateSlugAlwaysValid(t *testing.T) {
	t.Parallel()

	names := []string{
		"",                        // no name at all
		" ",                       // whitespace only
		"  \t\n ",                 // more whitespace
		"!",                       // one unusable rune
		"###",                     // all punctuation
		"---",                     // all hyphens
		"-",                       //
		"ok",                      // shorter than minSlugLen
		"a",                       // far shorter
		"42",                      // digits only, too short
		"2024",                    // digits only, long enough but no letter
		"123-456",                 // digits and hyphens, still no letter
		"Ω≈ç√",                    // non-ASCII only
		"Core Services",           // the ordinary case
		"  Core   Services  ",     // padded and double-spaced
		"3rd Party APIs",          // leading digit
		"2024 Recap",              // leading digits then a word
		"UPPER CASE",              // uppercase
		"Trailing hyphen -",       // trailing separator
		"- Leading hyphen",        // leading separator
		"a/b\\c:d?e#f",            // URL-hostile punctuation
		"emoji 🎉 name",            // emoji in the middle
		"🎉",                       // emoji only
		strings.Repeat("x", 500),  // way over maxSlugLen
		strings.Repeat("ab ", 80), // long, with separators near the cap
		strings.Repeat("-", 200),  // long and entirely unusable
	}

	for _, fallback := range []string{slugFallbackPage, slugFallbackSection} {
		require.Regexp(t, slugRegex, fallback, "the fallback itself must be a valid slug")

		for _, name := range names {
			got := generateSlug(name, fallback)

			require.Regexp(t, slugRegex, got,
				"generateSlug(%q, %q) produced %q, which the database CHECK constraint would reject",
				name, fallback, got)
			require.LessOrEqual(t, len(got), maxSlugLen, "generateSlug(%q) exceeded maxSlugLen", name)
			require.GreaterOrEqual(t, len(got), minSlugLen, "generateSlug(%q) fell under minSlugLen", name)
		}
	}
}

// TestGenerateSlugReadableForms documents the shapes we actually want out of
// the generator, as opposed to merely valid ones.
func TestGenerateSlugReadableForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "ordinary name", in: "Core Services", want: "core-services"},
		{name: "collapses separator runs", in: "Core   ///   Services", want: "core-services"},
		{name: "trims outer separators", in: "  -Core Services-  ", want: "core-services"},
		{name: "drops leading digits", in: "2024 Recap", want: "recap"},
		{name: "keeps interior digits", in: "Region eu 2", want: "region-eu-2"},
		{name: "lowercases", in: "API Gateway", want: "api-gateway"},
		{name: "unusable name falls back", in: "###", want: slugFallbackSection},
		{name: "too-short name falls back", in: "ok", want: slugFallbackSection},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, generateSlug(tt.in, slugFallbackSection))
		})
	}
}

// TestSuffixSlugRespectsMaxLen pins that disambiguation cannot push a slug past
// the length the constraint allows — the naive base+"-2" would.
func TestSuffixSlugRespectsMaxLen(t *testing.T) {
	t.Parallel()

	base := strings.Repeat("a", maxSlugLen)

	for _, n := range []int{2, 9, 10, 99} {
		got := suffixSlug(base, n)

		require.Regexp(t, slugRegex, got)
		require.LessOrEqual(t, len(got), maxSlugLen)
		require.True(t, strings.HasSuffix(got, "-"+itoaTest(n)), "suffix should be preserved, got %q", got)
	}

	// A short base is left alone apart from the suffix.
	require.Equal(t, "core-2", suffixSlug("core", 2))
}

func itoaTest(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}

	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// TestCreateSection_SlugHandling covers the four cases from the bug report:
// no slug, empty slug, whitespace-only slug, and a duplicate slug.
func TestCreateSection_SlugHandling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// seed is created first when non-empty, to set up collisions.
		seed        *CreateSectionRequest
		req         CreateSectionRequest
		wantSlug    string
		wantErr     error
		wantErrDesc string
	}{
		{
			name:     "no slug is generated from the name",
			req:      CreateSectionRequest{Name: "Core Services"},
			wantSlug: "core-services",
		},
		{
			name:     "empty slug is generated from the name",
			req:      CreateSectionRequest{Name: "Core Services", Slug: ""},
			wantSlug: "core-services",
		},
		{
			name:     "name that normalizes away falls back",
			req:      CreateSectionRequest{Name: "###"},
			wantSlug: slugFallbackSection,
		},
		{
			name:     "empty name falls back rather than failing",
			req:      CreateSectionRequest{Name: ""},
			wantSlug: slugFallbackSection,
		},
		{
			name:        "whitespace-only slug is rejected, not generated",
			req:         CreateSectionRequest{Name: "Core Services", Slug: "   "},
			wantErr:     ErrInvalidSlugFormat,
			wantErrDesc: "whitespace is far more likely a mistake than a request to auto-name",
		},
		{
			name:     "explicit slug is honored",
			req:      CreateSectionRequest{Name: "Core Services", Slug: "custom"},
			wantSlug: "custom",
		},
		{
			name:        "duplicate explicit slug conflicts",
			seed:        &CreateSectionRequest{Name: "First", Slug: "core"},
			req:         CreateSectionRequest{Name: "Second", Slug: "core"},
			wantErr:     ErrSlugConflict,
			wantErrDesc: "a slug the caller typed must never be silently renamed",
		},
		{
			name:     "duplicate generated slug is suffixed instead",
			seed:     &CreateSectionRequest{Name: "Core Services"},
			req:      CreateSectionRequest{Name: "Core Services"},
			wantSlug: "core-services-2",
		},
		{
			name:     "generated slug routes around an explicit one",
			seed:     &CreateSectionRequest{Name: "Whatever", Slug: "core-services"},
			req:      CreateSectionRequest{Name: "Core Services"},
			wantSlug: "core-services-2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx, svc, org := setupStatusPagesTest(t)

			page, err := svc.CreateStatusPage(ctx, org.Slug,
				&CreateStatusPageRequest{Name: "Public", Slug: testPublicSlug})
			r.NoError(err)

			if tt.seed != nil {
				_, errSeed := svc.CreateSection(ctx, org.Slug, page.UID, *tt.seed)
				r.NoError(errSeed)
			}

			section, err := svc.CreateSection(ctx, org.Slug, page.UID, tt.req)

			if tt.wantErr != nil {
				r.ErrorIs(err, tt.wantErr, tt.wantErrDesc)

				return
			}

			r.NoError(err)
			r.Equal(tt.wantSlug, section.Slug)
			r.Regexp(slugRegex, section.Slug)
		})
	}
}

// TestCreateStatusPage_SlugHandling mirrors the section cases. The status-page
// create path had the same defect and was not mentioned in the original
// report — a request with only a name returned the same 500.
func TestCreateStatusPage_SlugHandling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		seed     *CreateStatusPageRequest
		req      CreateStatusPageRequest
		wantSlug string
		wantErr  error
	}{
		{
			name:     "no slug is generated from the name",
			req:      CreateStatusPageRequest{Name: "Public Status"},
			wantSlug: "public-status",
		},
		{
			name:     "empty slug is generated from the name",
			req:      CreateStatusPageRequest{Name: "Public Status", Slug: ""},
			wantSlug: "public-status",
		},
		{
			name:     "unusable name falls back",
			req:      CreateStatusPageRequest{Name: "!!!"},
			wantSlug: slugFallbackPage,
		},
		{
			name:    "whitespace-only slug is rejected",
			req:     CreateStatusPageRequest{Name: "Public Status", Slug: "  "},
			wantErr: ErrInvalidSlugFormat,
		},
		{
			name:    "duplicate explicit slug conflicts",
			seed:    &CreateStatusPageRequest{Name: "First", Slug: "shared"},
			req:     CreateStatusPageRequest{Name: "Second", Slug: "shared"},
			wantErr: ErrSlugConflict,
		},
		{
			name:     "duplicate generated slug is suffixed",
			seed:     &CreateStatusPageRequest{Name: "Public Status"},
			req:      CreateStatusPageRequest{Name: "Public Status"},
			wantSlug: "public-status-2",
		},
		{
			name:    "uuid slug is still rejected",
			req:     CreateStatusPageRequest{Name: "Public", Slug: "3f2504e0-4f89-11d3-9a0c-0305e82c3301"},
			wantErr: ErrInvalidSlugFormat,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx, svc, org := setupStatusPagesTest(t)

			if tt.seed != nil {
				_, errSeed := svc.CreateStatusPage(ctx, org.Slug, tt.seed)
				r.NoError(errSeed)
			}

			req := tt.req
			page, err := svc.CreateStatusPage(ctx, org.Slug, &req)

			if tt.wantErr != nil {
				r.ErrorIs(err, tt.wantErr)

				return
			}

			r.NoError(err)
			r.Equal(tt.wantSlug, page.Slug)
			r.Regexp(slugRegex, page.Slug)
		})
	}
}

// TestCreateSection_GeneratedSlugSurvivesManyCollisions walks the suffix range
// far enough to prove the loop terminates and every step stays valid.
func TestCreateSection_GeneratedSlugSurvivesManyCollisions(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug,
		&CreateStatusPageRequest{Name: "Public", Slug: testPublicSlug})
	r.NoError(err)

	seen := map[string]bool{}

	for i := range 12 {
		section, errCreate := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{Name: "Core Services"})
		r.NoError(errCreate, "collision %d should be disambiguated, not rejected", i)
		r.Regexp(slugRegex, section.Slug)
		r.False(seen[section.Slug], "slug %q handed out twice", section.Slug)

		seen[section.Slug] = true
	}

	r.Len(seen, 12)
}

// TestCreateSection_NoRawDatabaseErrorForMissingSlug is the regression guard
// for the reported symptom itself. Before the fix this returned a driver error
// mentioning the constraint; the assertion is that whatever comes back, it is
// never a raw database error leaking schema internals.
func TestCreateSection_NoRawDatabaseErrorForMissingSlug(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug,
		&CreateStatusPageRequest{Name: "Public", Slug: testPublicSlug})
	r.NoError(err)

	section, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{Name: "Services"})
	r.NoError(err)
	r.Equal("services", section.Slug)

	// And the row really is readable back through the normal path.
	sections, err := svc.ListSections(ctx, org.Slug, page.UID)
	r.NoError(err)
	r.Len(sections, 1)
	r.Equal("services", sections[0].Slug)
}
