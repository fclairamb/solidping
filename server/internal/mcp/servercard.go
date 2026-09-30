package mcp

import (
	"net/http"
	"strings"

	"github.com/fclairamb/solidping/server/internal/version"
)

// ServerCardPath is the public, unauthenticated location of the static MCP
// server card (SEP-1649). Smithery reads it when it cannot introspect the live
// endpoint because /mcp requires OAuth.
const ServerCardPath = "/.well-known/mcp/server-card.json"

// Public listing facts advertised on the card. They are constants, never
// derived from a request, so every caller gets identical bytes.
const (
	serverCardTitle       = "SolidPing"
	serverCardDescription = "Uptime and incident monitoring: list, create and diagnose checks, " +
		"read incidents and results, and manage status pages and maintenance windows."
	serverCardHomepage      = "https://www.solidping.io"
	serverCardDocumentation = "https://docs.solidping.io/docs"
	serverCardRepository    = "https://github.com/fclairamb/solidping"
	serverCardLicense       = "AGPL-3.0"
	serverCardIconPath      = "/d/logo.png"
	serverCardConfigNote    = "No configuration is needed. Sign-in is OAuth 2.1 (dynamic client " +
		"registration), so there is no API key to paste."
)

// serverInstructions is the routing guidance returned in the `initialize`
// result and mirrored on the server card. It is static text: safe to serve
// anonymously and identical for every caller.
const serverInstructions = `SolidPing monitors uptime and manages incidents and status pages.
Start with list_checks (what is monitored) or list_incidents (what is broken now).
Use diagnose_check for "why is X down": it explains the latest failures of one check.
Run validate_check before create_check to catch a bad config without saving anything.
Use get_check_type_samples to learn the config a check type expects, and list_check_types to see the types.
Use list_results for the raw history of one check, and list_regions to pick where it runs.
Status page, incident publication, maintenance window and integration writes need the mcp scope
(mcp:read tokens are read-only).
Every tool is scoped to the organization the token was issued for.`

// serverCard is the JSON document served at ServerCardPath. Its keys follow
// https://smithery.ai/docs/build/external (serverInfo, authentication, tools,
// resources, prompts, configSchema); the remaining metadata keys come from
// SEP-1649 and are ignored by readers that do not know them.
type serverCard struct {
	Schema         string               `json:"$schema,omitempty"`
	ServerInfo     serverCardInfo       `json:"serverInfo"`
	Description    string               `json:"description"`
	IconURL        string               `json:"iconUrl,omitempty"`
	Documentation  string               `json:"documentationUrl"`
	Instructions   string               `json:"instructions"`
	Authentication serverCardAuth       `json:"authentication"`
	ConfigSchema   map[string]any       `json:"configSchema"`
	Tools          []ToolDefinition     `json:"tools"`
	Resources      []ResourceDefinition `json:"resources"`
	Prompts        []PromptDefinition   `json:"prompts"`
}

type serverCardInfo struct {
	Name       string `json:"name"`
	Title      string `json:"title"`
	Version    string `json:"version"`
	WebsiteURL string `json:"websiteUrl"`
	Repository string `json:"repository"`
	License    string `json:"license"`
}

type serverCardAuth struct {
	Required bool     `json:"required"`
	Schemes  []string `json:"schemes"`
}

// buildServerCard assembles the card from the same registry the authenticated
// endpoint serves, so tools/list and the card cannot drift. It takes no request:
// nothing per-caller or per-org can reach it.
func (h *Handler) buildServerCard() serverCard {
	iconURL := ""
	if h.baseURL != "" {
		iconURL = strings.TrimRight(h.baseURL, "/") + serverCardIconPath
	}

	return serverCard{
		ServerInfo: serverCardInfo{
			Name:       "solidping",
			Title:      serverCardTitle,
			Version:    version.Version,
			WebsiteURL: serverCardHomepage,
			Repository: serverCardRepository,
			License:    serverCardLicense,
		},
		Description:    serverCardDescription,
		IconURL:        iconURL,
		Documentation:  serverCardDocumentation,
		Instructions:   serverInstructions,
		Authentication: serverCardAuth{Required: true, Schemes: []string{"oauth2"}},
		ConfigSchema: map[string]any{
			schemaKeyType:          schemaTypeObject,
			schemaKeyDescription:   serverCardConfigNote,
			schemaKeyProperties:    map[string]any{},
			"additionalProperties": false,
		},
		Tools:     h.tools,
		Resources: h.getResourceDefinitions(),
		Prompts:   listPrompts(),
	}
}

// HandleServerCard serves the static server card. It is unauthenticated by
// design (spec 2026-09-30-03): the tool surface is already public in the docs,
// and the card is the only anonymous surface beyond the initialize handshake.
// tools/list and every tools/call stay behind RequireMCPAuth.
func (h *Handler) HandleServerCard(writer http.ResponseWriter, _ *http.Request) error {
	writer.Header().Set("Cache-Control", "public, max-age=300")

	return writeJSON(writer, http.StatusOK, h.buildServerCard())
}
