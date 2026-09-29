package mcp

import (
	"net/http"

	"github.com/fclairamb/solidping/server/internal/version"
)

// PathServerCard is where MCP directories look for a server card (SEP-1649).
// It sits at the site root, next to the OAuth discovery documents.
const PathServerCard = "/.well-known/mcp/server-card.json"

// serverCardAuthentication tells a directory how a client gets in. The MCP
// endpoint answers 401 with a resource_metadata pointer, and the OAuth 2.1
// authorization server behind it is described by the neighboring
// /.well-known/oauth-* documents.
type serverCardAuthentication struct {
	Required bool     `json:"required"`
	Schemes  []string `json:"schemes"`
}

// serverCard is the static description of this MCP server. It exists because
// tools/list needs a credential: a directory that cannot authenticate would
// otherwise list a server with no capabilities. Everything in it comes from
// the same registries that answer tools/list, prompts/list and resources/list,
// so it cannot drift from what an authenticated client sees, and none of it is
// organization data.
type serverCard struct {
	ServerInfo     ServerInfo               `json:"serverInfo"`
	Authentication serverCardAuthentication `json:"authentication"`
	Tools          []ToolDefinition         `json:"tools"`
	Resources      []ResourceDefinition     `json:"resources"`
	Prompts        []PromptDefinition       `json:"prompts"`
}

// HandleServerCard serves the server card. It is deliberately outside the MCP
// auth middleware and answers nothing but this fixed document.
func (h *Handler) HandleServerCard(writer http.ResponseWriter, _ *http.Request) error {
	writer.Header().Set("Cache-Control", "public, max-age=300")

	return writeJSON(writer, http.StatusOK, serverCard{
		ServerInfo:     ServerInfo{Name: "solidping", Version: version.Version},
		Authentication: serverCardAuthentication{Required: true, Schemes: []string{"oauth2", "bearer"}},
		Tools:          h.tools,
		Resources:      h.getResourceDefinitions(),
		Prompts:        listPrompts(),
	})
}
