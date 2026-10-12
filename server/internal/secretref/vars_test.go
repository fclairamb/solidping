package secretref_test

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/secretref"
)

func TestVarFormats(t *testing.T) {
	t.Parallel()

	// 14:30:05.123 in UTC+2: the generated values are UTC.
	now := time.Date(2026, 10, 12, 16, 30, 5, 123_000_000, time.FixedZone("CEST", 2*3600))

	tests := []struct {
		name    string
		pattern string
		exact   string
	}{
		{name: "timestamp", pattern: `^\d{17}$`, exact: "20261012143005123"},
		{name: "iso8601", pattern: `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`, exact: "2026-10-12T14:30:05Z"},
		{name: "uuid", pattern: `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`},
		{name: "random", pattern: `^[a-z0-9]{8}$`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			out, _, err := secretref.ResolveString(t.Context(), "${var:"+tc.name+"}",
				secretref.NewVarResolver(now, rand.Reader))
			r.NoError(err)
			r.Regexp(regexp.MustCompile(tc.pattern), out)

			if tc.exact != "" {
				r.Equal(tc.exact, out)
			}
		})
	}
}

func TestVarSameValueWithinOneWalk(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	out, replaced, err := secretref.ResolveConfig(t.Context(), map[string]any{
		"url":     "https://acme.com/?cb=${var:uuid}",
		"headers": map[string]any{"X-Request-Id": "${var:uuid}"},
	}, secretref.ExecutionResolver())
	r.NoError(err)
	r.True(replaced)

	id, ok := out["headers"].(map[string]any)["X-Request-Id"].(string)
	r.True(ok)
	r.Len(id, 36)
	r.Equal("https://acme.com/?cb="+id, out["url"])
}

func TestVarNewValuesPerExecutionResolver(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	resolve := func() (string, string) {
		out, _, err := secretref.ResolveString(t.Context(), "${var:uuid} ${var:random}",
			secretref.ExecutionResolver())
		r.NoError(err)

		var id, rnd string

		_, scanErr := fmt.Sscan(out, &id, &rnd)
		r.NoError(scanErr)

		return id, rnd
	}

	id1, rnd1 := resolve()
	id2, rnd2 := resolve()
	r.NotEqual(id1, id2)
	r.NotEqual(rnd1, rnd2)
}

func TestVarUnknownNameFails(t *testing.T) {
	t.Parallel()

	for _, ref := range []string{"${var:nope}", "${var:UUID}"} {
		t.Run(ref, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			_, _, err := secretref.ResolveString(t.Context(), ref, secretref.ExecutionResolver())
			r.ErrorIs(err, secretref.ErrUnresolved)

			_, _, err = secretref.ResolveString(t.Context(), ref, secretref.DocumentResolver(nil, "org-1"))
			r.ErrorIs(err, secretref.ErrUnresolved)
		})
	}
}

func TestDocumentResolverAcceptsKnownVar(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	out, _, err := secretref.ResolveString(t.Context(), "${var:timestamp}", secretref.DocumentResolver(nil, "org-1"))
	r.NoError(err)
	r.Len(out, 17)
}

func TestVarsOnlyResolverLeavesSecretsLiteral(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	out, _, err := secretref.ResolveString(t.Context(), "?t=${param:tok}&e=${env:X}&cb=${var:timestamp}",
		secretref.VarsOnlyResolver(secretref.NewVarResolver(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), rand.Reader)))
	r.NoError(err)
	r.Equal("?t=${param:tok}&e=${env:X}&cb=20260102030405000", out)
}

func TestKnownVars(t *testing.T) {
	t.Parallel()
	require.Equal(t, []string{"timestamp", "iso8601", "uuid", "random"}, secretref.KnownVars())
}
