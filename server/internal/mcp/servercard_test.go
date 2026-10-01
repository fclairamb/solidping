package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func serveCard(ctx context.Context, t *testing.T, handler *Handler) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, ServerCardPath, nil)
	rec := httptest.NewRecorder()
	require.NoError(t, handler.HandleServerCard(rec, req))

	return rec
}

func TestServerCard_ServedWithoutCredentials(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rec := serveCard(t.Context(), t, newTestHandler())
	r.Equal(http.StatusOK, rec.Code)
	r.Contains(rec.Header().Get("Content-Type"), "application/json")

	var card map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &card))

	info, ok := card["serverInfo"].(map[string]any)
	r.True(ok)
	r.Equal("solidping", info["name"])
	r.NotEmpty(info["version"])

	auth, ok := card["authentication"].(map[string]any)
	r.True(ok)
	r.Equal(true, auth["required"])
	r.Equal([]any{"oauth2"}, auth["schemes"])

	r.NotEmpty(card["instructions"])
	r.NotEmpty(card["prompts"])
	r.NotEmpty(card["resources"])
}

func TestServerCard_ToolsMatchTheRegistry(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	rec := serveCard(t.Context(), t, handler)

	var card struct {
		Tools []ToolDefinition `json:"tools"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &card))

	got := map[string]bool{}
	for i := range card.Tools {
		got[card.Tools[i].Name] = true
	}

	want := map[string]bool{}
	for i := range handler.tools {
		want[handler.tools[i].Name] = true
	}

	r.Equal(want, got, "the card must list exactly the registered tools")
	r.True(got["list_checks"], "positive control: a known tool is present")
	r.False(got["not_a_registered_tool"], "negative control: an unregistered name is absent")
}

func TestServerCard_EveryToolCarriesItsMetadata(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	rec := serveCard(t.Context(), t, handler)

	var card struct {
		Tools []map[string]any `json:"tools"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &card))
	r.Len(card.Tools, len(handler.tools))

	registry := map[string]ToolDefinition{}
	for i := range handler.tools {
		registry[handler.tools[i].Name] = handler.tools[i]
	}

	withOutput := 0
	for _, tool := range card.Tools {
		name, _ := tool["name"].(string)
		r.NotNil(tool["annotations"], "tool %q lacks annotations", name)
		r.NotNil(tool["inputSchema"], "tool %q lacks inputSchema", name)

		if registry[name].OutputSchema != nil {
			withOutput++
			r.NotNil(tool["outputSchema"], "tool %q lost its outputSchema", name)
		}
	}

	r.Positive(withOutput, "positive control: some tools declare an outputSchema")
}

func TestServerCard_ConfigSchemaHasNoRequiredFields(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rec := serveCard(t.Context(), t, newTestHandler())

	var card map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &card))

	schema, ok := card["configSchema"].(map[string]any)
	r.True(ok, "configSchema must be present")
	r.Equal("object", schema["type"])
	r.NotContains(schema, "required")
	r.Empty(schema["properties"])
	r.NotEmpty(schema["description"])
}

type ctxKeyProbe struct{}

func TestServerCard_IsIdenticalForEveryCaller(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()

	anonymous := serveCard(t.Context(), t, handler)
	authed := serveCard(context.WithValue(t.Context(), ctxKeyProbe{}, defaultClaims()), t, handler)
	otherOrg := serveCard(context.WithValue(t.Context(), ctxKeyProbe{}, "another-org"), t, handler)

	r.Equal(anonymous.Body.String(), authed.Body.String())
	r.Equal(anonymous.Body.String(), otherOrg.Body.String())
}
