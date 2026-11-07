package scrub_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/scrub"
)

func TestTextRedactsEmbeddedURLs(t *testing.T) {
	t.Parallel()

	out := scrub.Text(`get "https://x.example/cb?code=abc&page=2#access_token=zzz": boom`)
	require.NotContains(t, out, "abc")
	require.NotContains(t, out, "zzz")
	require.Contains(t, out, "page=2")
	require.Contains(t, out, "boom")
}

func TestQueryKeepsOtherParams(t *testing.T) {
	t.Parallel()
	require.Equal(t, "a=1&token=[FILTERED]", scrub.Query("a=1&token=s3"))
}
