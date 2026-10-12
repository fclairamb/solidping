package checkerdef

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSamplesToSeed(t *testing.T) {
	t.Parallel()

	samples := []CheckSpec{
		{Slug: "public-a", SeedOnInstall: true},
		{Slug: "local-service"},
		{Slug: "public-b", SeedOnInstall: true},
	}

	seedsOnly := []string{"public-a", "public-b"}
	everything := []string{"public-a", "local-service", "public-b"}

	tests := []struct {
		name string
		opts *ListSampleOptions
		want []string
	}{
		{name: "default keeps only seeds", opts: &ListSampleOptions{Type: Default}, want: seedsOnly},
		{name: "nil options behave as default", opts: nil, want: seedsOnly},
		{name: "test seeds everything", opts: &ListSampleOptions{Type: Test}, want: everything},
		{name: "demo seeds everything", opts: &ListSampleOptions{Type: Demo}, want: everything},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			seeds := SamplesToSeed(tc.opts, samples)

			slugs := make([]string, 0, len(seeds))
			for i := range seeds {
				slugs = append(slugs, seeds[i].Slug)
			}

			r.Equal(tc.want, slugs)
		})
	}
}

func TestSamplesToSeedEmpty(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Empty(SamplesToSeed(&ListSampleOptions{Type: Default}, nil))
	r.Empty(SamplesToSeed(&ListSampleOptions{Type: Default}, []CheckSpec{{Slug: "local-service"}}))
}
