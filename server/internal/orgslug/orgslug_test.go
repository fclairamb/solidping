package orgslug_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/orgslug"
)

// fakeFinder reports a slug as taken if it appears in the taken set.
type fakeFinder struct {
	taken map[string]bool
}

func (f *fakeFinder) GetOrganizationBySlug(_ context.Context, slug string) (*models.Organization, error) {
	if f.taken[slug] {
		return &models.Organization{Slug: slug}, nil
	}

	return nil, sql.ErrNoRows
}

func TestSlugify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "spaces and case", input: "Acme Corp", want: "acme-corp"},
		{name: "already a slug", input: "acme", want: "acme"},
		{name: "emoji only", input: "🚀", want: ""},
		{name: "too short", input: "a", want: ""},
		{name: "surrounding hyphens", input: "--Foo--", want: "foo"},
		{name: "collapse multiple spaces", input: "a   b", want: "a-b"},
		{name: "empty", input: "", want: ""},
		{
			name:  "thirty chars capped at twenty",
			input: "abcdefghijklmnopqrstuvwxyz1234",
			want:  "abcdefghijklmnopqrst",
		},
		{
			name:  "cap leaves no trailing hyphen",
			input: "aaaaaaaaaaaaaaaaaaaa-bbbbb",
			want:  "aaaaaaaaaaaaaaaaaaaa",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			got := orgslug.Slugify(tc.input)
			r.Equal(tc.want, got)

			if got != "" {
				r.LessOrEqual(len(got), 20)
				r.GreaterOrEqual(len(got), 3)
				r.NotEqual(byte('-'), got[len(got)-1])
				r.NotEqual(byte('-'), got[0])
			}
		})
	}
}

func TestGenerateUnique(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		candidates []string
		taken      map[string]bool
		want       string
	}{
		{
			name:       "picks first usable candidate",
			candidates: []string{"", "acme", "x"},
			want:       "acme",
		},
		{
			name:       "skips too-short first candidate",
			candidates: []string{"a", "acme"},
			want:       "acme",
		},
		{
			name:       "all fail falls back to org",
			candidates: []string{"", "a", "🚀"},
			want:       "org",
		},
		{
			name:       "no candidates falls back to org",
			candidates: nil,
			want:       "org",
		},
		{
			name:       "appends 2 on collision",
			candidates: []string{"acme"},
			taken:      map[string]bool{"acme": true},
			want:       "acme2",
		},
		{
			name:       "appends 3 when 2 also taken",
			candidates: []string{"acme"},
			taken:      map[string]bool{"acme": true, "acme2": true},
			want:       "acme3",
		},
		{
			name:       "uses team_domain over team_name",
			candidates: []string{"acme", "Acme Corp"},
			want:       "acme",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			finder := &fakeFinder{taken: tc.taken}
			got := orgslug.GenerateUnique(context.Background(), finder, tc.candidates...)
			r.Equal(tc.want, got)
		})
	}
}

func TestGenerateUniqueLongBaseWithSuffixStaysUnder20(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// A 20-char base whose plain form is already taken: the collision suffix
	// must truncate the base so base+suffix stays <= 20 chars.
	longBase := "abcdefghijklmnopqrst" // exactly 20 chars
	finder := &fakeFinder{taken: map[string]bool{longBase: true}}

	got := orgslug.GenerateUnique(context.Background(), finder, longBase)
	r.LessOrEqual(len(got), 20)
	r.Equal("abcdefghijklmnopqrs2", got)
}

// TestIsValid pins the organization-slug rule itself. This package is the
// single source of truth for it: org creation (auth.CreateOrg), org rename
// (auth org profile), the SPA rename-redirect pre-filter and the Telegram
// org-qualified-reference parser all call IsValid rather than restating the
// pattern. Loosening the rule here is a deliberate act that shows up as a
// failure in this table.
func TestIsValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		slug string
		want bool
	}{
		{name: "minimum length", slug: "abc", want: true},
		{name: "maximum length", slug: "abcdefghij0123456789", want: true},
		{name: "digits only", slug: "123", want: true},
		{name: "internal hyphens", slug: "a-b-c", want: true},
		{name: "digit at both ends", slug: "1acme2", want: true},

		{name: "too short", slug: "ab", want: false},
		{name: "too long", slug: "abcdefghij01234567890", want: false},
		{name: "empty", slug: "", want: false},
		{name: "leading hyphen", slug: "-acme", want: false},
		{name: "trailing hyphen", slug: "acme-", want: false},
		{name: "uppercase", slug: "Acme", want: false},
		{name: "underscore", slug: "my_org", want: false},
		{name: "dot", slug: "my.org", want: false},
		{name: "space", slug: "my org", want: false},
		{name: "slash", slug: "my/org", want: false},
		{name: "unicode", slug: "acmé", want: false},
		{name: "newline smuggled past the anchors", slug: "acme\nx", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			r.Equal(tt.want, orgslug.IsValid(tt.slug))
		})
	}
}

// TestIsValidBoundsMatchTheExportedLengths keeps the regex and the generator's
// MinLen/MaxLen from drifting: the regex is built from those constants, and a
// slug of exactly each bound must be accepted while one character either side
// must not.
func TestIsValidBoundsMatchTheExportedLengths(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	atMin := strings.Repeat("a", orgslug.MinLen)
	atMax := strings.Repeat("a", orgslug.MaxLen)

	r.True(orgslug.IsValid(atMin), "a slug of exactly MinLen must be valid")
	r.True(orgslug.IsValid(atMax), "a slug of exactly MaxLen must be valid")
	r.False(orgslug.IsValid(strings.Repeat("a", orgslug.MinLen-1)), "one below MinLen must be rejected")
	r.False(orgslug.IsValid(strings.Repeat("a", orgslug.MaxLen+1)), "one above MaxLen must be rejected")
}

// TestGeneratedSlugsAreAlwaysValid is the property that ties the generator to
// the rule: anything Slugify or GenerateUnique produces must be something the
// validators accept. Without it, the auto-create flows (Slack sign-in, Slack
// install, Discord) could mint an organization whose slug its own API would
// refuse — and which could never be used in a Telegram qualified reference.
func TestGeneratedSlugsAreAlwaysValid(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	candidates := []string{
		"Acme Corp",
		"acme",
		"--Foo--",
		"a   b",
		"A Very Long Workspace Name That Overflows The Cap",
		"team-2026",
		"UPPER CASE ORG",
		"weird!!!chars###here",
		"trailing-hyphen-after-cap-aaaa",
		"123456",
	}

	for _, candidate := range candidates {
		if slug := orgslug.Slugify(candidate); slug != "" {
			r.True(orgslug.IsValid(slug), "Slugify(%q) = %q must satisfy IsValid", candidate, slug)
		}
	}

	// GenerateUnique appends collision suffixes and re-caps; the result must
	// still satisfy the rule.
	finder := &fakeFinder{taken: map[string]bool{}}
	for _, candidate := range candidates {
		slug := orgslug.GenerateUnique(context.Background(), finder, candidate)
		r.True(orgslug.IsValid(slug), "GenerateUnique(%q) = %q must satisfy IsValid", candidate, slug)
		finder.taken[slug] = true
	}

	// And again with every previous slug taken, forcing the suffix path.
	for _, candidate := range candidates {
		slug := orgslug.GenerateUnique(context.Background(), finder, candidate)
		r.True(orgslug.IsValid(slug), "GenerateUnique(%q) = %q must satisfy IsValid on collision", candidate, slug)
		finder.taken[slug] = true
	}
}
