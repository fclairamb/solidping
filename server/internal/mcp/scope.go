package mcp

import (
	"strings"

	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

// MCP token scopes. A token bearing "mcp" can call any tool; "mcp:read"
// is restricted to non-mutating tools.
const (
	scopeMCP     = "mcp"
	scopeMCPRead = "mcp:read"
)

// Prefixes used by tool-name conventions for write operations. Read-only
// tokens (mcp:read) are refused on any tool whose name begins with one of
// these. Deny-list (rather than per-tool annotations) is the pragmatic v1
// — every new write tool naturally falls under one of these prefixes, and
// a stray miss is a reviewable oversight, not a silent escalation.
const (
	mutationPrefixCreate = "create_"
	mutationPrefixUpdate = "update_"
	mutationPrefixDelete = "delete_"
	mutationPrefixSet    = "set_"
)

// hasMCPAccess decides whether a credential is allowed to use the MCP
// endpoint at all. Empty Scopes is treated as a full user session
// (back-compat for dashboard JWTs that pre-date scopes); otherwise the
// scope list must include "mcp" or "mcp:read".
func hasMCPAccess(claims *auth.Claims) bool {
	if claims == nil {
		return false
	}
	if len(claims.Scopes) == 0 {
		return true
	}
	for _, s := range claims.Scopes {
		if s == scopeMCP || s == scopeMCPRead {
			return true
		}
	}
	return false
}

// isMCPReadOnly returns true when the credential has mcp:read but not the
// broader "mcp" scope. Empty scopes (full session) are not read-only.
func isMCPReadOnly(claims *auth.Claims) bool {
	if claims == nil || len(claims.Scopes) == 0 {
		return false
	}
	hasRead := false
	for _, s := range claims.Scopes {
		if s == scopeMCP {
			return false
		}
		if s == scopeMCPRead {
			hasRead = true
		}
	}
	return hasRead
}

// isMutationTool returns true when the named tool performs writes and
// should be denied to mcp:read callers.
func isMutationTool(name string) bool {
	return strings.HasPrefix(name, mutationPrefixCreate) ||
		strings.HasPrefix(name, mutationPrefixUpdate) ||
		strings.HasPrefix(name, mutationPrefixDelete) ||
		strings.HasPrefix(name, mutationPrefixSet)
}
