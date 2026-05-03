package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseLabelsArg(t *testing.T) {
	t.Parallel()

	t.Run("nil-typed object → empty map", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		out, errMsg := parseLabelsArg(map[string]any{})
		r.Empty(errMsg)
		r.NotNil(out)
		r.Empty(out)
	})

	t.Run("string values are forwarded", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		out, errMsg := parseLabelsArg(map[string]any{
			"env":  "prod",
			"team": "api",
		})
		r.Empty(errMsg)
		r.Equal(map[string]string{"env": "prod", "team": "api"}, out)
	})

	t.Run("non-object (string) is rejected with helpful message", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		out, errMsg := parseLabelsArg("env:prod")
		r.Nil(out)
		r.Contains(errMsg, "labels must be a JSON object")
	})

	t.Run("non-object (array) is rejected", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		out, errMsg := parseLabelsArg([]any{"env", "prod"})
		r.Nil(out)
		r.Contains(errMsg, "labels must be a JSON object")
	})

	t.Run("non-string value is rejected with key in message", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		out, errMsg := parseLabelsArg(map[string]any{"env": 123})
		r.Nil(out)
		r.Contains(errMsg, "labels.env must be a string")
	})

	t.Run("nil value is rejected", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		out, errMsg := parseLabelsArg(map[string]any{"env": nil})
		r.Nil(out)
		r.Contains(errMsg, "labels.env must be a string")
	})
}

func TestToolListChecks_LabelsTypeError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	result := handler.toolListChecks(context.Background(), "test-org", map[string]any{
		"labels": "env:prod", // legacy string form, now rejected
	})
	r.True(result.IsError)
	r.Contains(result.Content[0].Text, "labels must be a JSON object")
}

func TestListChecksDef_LabelsIsObject(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	def := listChecksDef()
	schema, ok := def.InputSchema.(map[string]any)
	r.True(ok)
	props, ok := schema["properties"].(map[string]any)
	r.True(ok)
	labelsProp, ok := props[propLabels].(map[string]any)
	r.True(ok)
	r.Equal("object", labelsProp[schemaKeyType])
	desc, ok := labelsProp[schemaKeyDescription].(string)
	r.True(ok)
	r.Contains(desc, "JSON object")
	r.Contains(desc, "env")
}
