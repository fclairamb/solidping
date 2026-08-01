package importers

import (
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// slugRule is the exact slug format the checks service enforces on import.
var slugRule = regexp.MustCompile(`^[a-z][a-z0-9-]{2,49}$`)

func TestSlugifyAlwaysProducesAValidSlug(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cases := []struct {
		in   string
		want string
	}{
		{"Back-end health", "back-end-health"},
		{"  API / Gateway  ", "api-gateway"},
		{"123 numeric start", "x123-numeric-start"},
		{"", "x-x"},
		{"!!!", "x-x"},
		{"a", "a-x"},
		{"UPPER_snake.case", "upper-snake-case"},
		{
			"a-very-long-name-that-goes-well-past-the-fifty-character-slug-limit-for-sure",
			"a-very-long-name-that-goes-well-past-the-fifty-cha",
		},
	}

	for _, tc := range cases {
		got := slugify(tc.in)
		r.Equal(tc.want, got, "slugify(%q)", tc.in)
		r.Regexp(slugRule, got, "slugify(%q) must satisfy the check slug rule", tc.in)
	}
}

func TestSlugSetDedupes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	set := newSlugSet()
	r.Equal("api", set.unique("API"))
	r.Equal("api-2", set.unique("api"))
	r.Equal("api-3", set.unique("Api"))
	r.Regexp(slugRule, set.unique("api"))
}

func TestSlugSetDedupeRespectsLengthCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	set := newSlugSet()
	long := "endpoint-name-that-is-exactly-at-the-fifty-character-cap-here"
	first := set.unique(long)
	second := set.unique(long)

	r.NotEqual(first, second)
	r.Regexp(slugRule, first)
	r.Regexp(slugRule, second)
	r.LessOrEqual(len(second), maxSlugLen)
}

func TestSecondsToPeriod(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Empty(secondsToPeriod(0))
	r.Empty(secondsToPeriod(-5))
	r.Equal("30s", secondsToPeriod(30))
	r.Equal("5m", secondsToPeriod(300))
	r.Equal("1h", secondsToPeriod(3600))
	r.Equal("90s", secondsToPeriod(90))
}

func TestDurationToPeriod(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Empty(durationToPeriod(0))
	r.Equal("45s", durationToPeriod(45*time.Second))
	r.Equal("2m", durationToPeriod(2*time.Minute))
	r.Equal("3h", durationToPeriod(3*time.Hour))
}
