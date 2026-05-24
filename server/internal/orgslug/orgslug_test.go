package orgslug_test

import (
	"context"
	"database/sql"
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
