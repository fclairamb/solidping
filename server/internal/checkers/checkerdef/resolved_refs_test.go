package checkerdef

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolvedReferenceValuesRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Nil(ResolvedReferenceValuesFrom(context.Background()))

	ctx := WithResolvedReferenceValues(context.Background(), []string{"", "k3y", ""})
	r.Equal([]string{"k3y"}, ResolvedReferenceValuesFrom(ctx))

	// Only empty strings: nothing is put on the context.
	r.Nil(ResolvedReferenceValuesFrom(WithResolvedReferenceValues(context.Background(), []string{""})))

	r.True(ContainsResolvedReferenceValue("Bearer k3y", []string{"k3y"}))
	r.False(ContainsResolvedReferenceValue("Bearer other", []string{"k3y", ""}))
	r.False(ContainsResolvedReferenceValue("anything", nil))
}
