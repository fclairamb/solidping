package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandleServerCard(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, PathServerCard, nil)

	r.NoError(handler.HandleServerCard(rec, req))
	r.Equal(http.StatusOK, rec.Code)
	r.Contains(rec.Header().Get("Content-Type"), "application/json")

	var card struct {
		ServerInfo     ServerInfo `json:"serverInfo"`
		Authentication struct {
			Required bool     `json:"required"`
			Schemes  []string `json:"schemes"`
		} `json:"authentication"`
		Tools     []map[string]any `json:"tools"`
		Resources []map[string]any `json:"resources"`
		Prompts   []map[string]any `json:"prompts"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &card))

	r.Equal("solidping", card.ServerInfo.Name)
	r.NotEmpty(card.ServerInfo.Version)
	r.True(card.Authentication.Required)
	r.Contains(card.Authentication.Schemes, "oauth2")
	r.NotEmpty(card.Tools)
	r.NotEmpty(card.Prompts)

	// A directory needs name, description and an object inputSchema for every
	// tool: that is what its score is computed from.
	for _, tool := range card.Tools {
		name, _ := tool["name"].(string)
		r.NotEmpty(name)
		r.NotEmpty(tool["description"], "tool %s has no description", name)
		schema, ok := tool["inputSchema"].(map[string]any)
		r.True(ok, "tool %s has no inputSchema object", name)
		r.Equal("object", schema["type"], "tool %s inputSchema.type", name)
	}
}

func TestServerCardMatchesToolsList(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, PathServerCard, nil)
	r.NoError(handler.HandleServerCard(rec, req))

	var card struct {
		Tools []ToolDefinition `json:"tools"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &card))
	r.Len(card.Tools, len(handler.tools), "the card must list exactly what tools/list answers")
}
