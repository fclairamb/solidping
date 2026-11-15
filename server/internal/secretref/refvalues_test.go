package secretref_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/secretref"
)

func refTestResolver(values map[string]string) secretref.ResolveFunc {
	return func(_ context.Context, scheme, name string) (string, error) {
		if scheme == secretref.SchemeVar {
			return "generated", nil
		}

		value, ok := values[scheme+":"+name]
		if !ok {
			return "", secretref.ErrSkip
		}

		return value, nil
	}
}

// The values every env/param reference of a config resolved to are recorded,
// nested maps included; a var value is not.
func TestCollectingResolverReportsResolvedValues(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	config := map[string]any{
		"headers": map[string]any{"X": "${param:k}"},
		"url":     "https://a/?t=${param:k2}&c=${var:uuid}",
		"env":     map[string]any{"E": "${env:E}"},
	}

	var values []string

	resolved, replaced, err := secretref.ResolveConfig(context.Background(), config,
		secretref.CollectingResolver(refTestResolver(map[string]string{
			"param:k": "v-one", "param:k2": "v-two", "env:E": "v-env",
		}), &values))
	r.NoError(err)
	r.True(replaced)
	r.ElementsMatch([]string{"v-one", "v-two", "v-env"}, values)
	r.Equal("https://a/?t=v-two&c=generated", resolved["url"])

	// No reference, no value.
	values = nil

	_, _, err = secretref.ResolveConfig(context.Background(),
		map[string]any{"url": "https://a/", "headers": map[string]any{"X": "plain"}},
		secretref.CollectingResolver(refTestResolver(nil), &values))
	r.NoError(err)
	r.Empty(values)
}

// ReferenceValues recovers the values from a template and its resolved copy:
// what the worker does with the server-resolved ${param:…} overlay.
func TestReferenceValuesFromTemplate(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	template := map[string]any{
		"headers": map[string]any{"X": "${param:k}", "Authorization": "Bearer ${param:tok}"},
		"url":     "https://a/?t=${param:k2}&c=${var:uuid}",
		"list":    []any{"a-${env:E}-b", "plain"},
		"same":    "${param:a}${param:b}",
		"skipped": "${env:LEFT}",
	}
	resolved := map[string]any{
		"headers": map[string]any{"X": "v-one", "Authorization": "Bearer v-tok"},
		"url":     "https://a/?t=v-two&c=0000",
		"list":    []any{"a-v-env-b", "plain"},
		"same":    "ab",
		"skipped": "${env:LEFT}",
	}

	r.ElementsMatch([]string{"v-one", "v-tok", "v-two", "v-env", "ab"},
		secretref.ReferenceValues(template, resolved))

	// Literal text that does not match: nothing is guessed.
	r.Empty(secretref.ReferenceValues(
		map[string]any{"url": "https://a/?t=${param:k}"},
		map[string]any{"url": "https://other/"}))

	// No reference: none.
	r.Empty(secretref.ReferenceValues(
		map[string]any{"url": "https://a/"}, map[string]any{"url": "https://a/"}))
}
