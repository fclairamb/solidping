package mcp

import (
	"errors"

	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// demoAllowedMutationTools is the COMPLETE set of mutation tools a demo
// session (claims.Demo) may call. It mirrors the REST allowlist in
// handlers/auth/demo_guard.go: creating, editing and deleting checks is the
// demo, everything else — status pages, integrations, maintenance windows,
// incident publications, every set_* tool — is refused.
//
// It is an allowlist for the same reason the REST one is: a mutation tool
// added next year is refused for demo sessions on the day it is registered,
// without anybody having to remember this file exists.
//
// Ownership is NOT decided here. update_check and delete_check reach
// checks.Service, which refuses a check this session did not create
// (assertDemoMayWriteCheck, keyed off the claims RequireMCPAuth put on the
// context), and create_check goes through the same demo shape rules and
// created_by stamping as POST /checks.
//
// The unprefixed tools are all reads and need no entry. diagnose_check reads
// the check, its recent results and its incidents; validate_check is the same
// dry run as the allowlisted POST /checks/validate and persists nothing. A
// future unprefixed tool that persists state must either take a mutation
// prefix or be refused for demo sessions explicitly.
//
//nolint:gochecknoglobals // Effectively a constant set; Go has no const maps.
var demoAllowedMutationTools = map[string]struct{}{
	toolCreateCheck: {},
	toolUpdateCheck: {},
	toolDeleteCheck: {},
}

// demoToolRefused is the demo half of the tools/call gate, next to the scope
// and role gates. It reports whether a demo session must be refused this tool.
//
// The decision lives here rather than in RequireMCPAuth because MCP is
// JSON-RPC over a single POST route: the HTTP method and route pattern say
// nothing about whether a call reads or writes. Only the tool name does.
// Non-tool methods (initialize, tools/list, resources/*, prompts/*) are reads
// and never reach this function.
func demoToolRefused(claims *auth.Claims, tool string) bool {
	if claims == nil || !claims.Demo || !isMutationTool(tool) {
		return false
	}

	_, allowed := demoAllowedMutationTools[tool]

	return !allowed
}

// demoRefusalResponse is the JSON-RPC refusal for a demo session. The message
// is auth.DemoWriteMessage and data.code is DEMO_READ_ONLY, so an MCP refusal
// reads exactly like the REST one, to a human and to a program.
func demoRefusalResponse(id any) Response {
	resp := errorResponse(id, CodeForbidden, auth.DemoWriteMessage)
	resp.Error.Data = map[string]string{"code": string(base.ErrorCodeDemoReadOnly)}

	return resp
}

// checkWriteErrorResult turns a checks.Service write error into a tool error.
// The ownership refusal (a demo session touching a check it did not create)
// is reworded to auth.DemoWriteMessage, the text REST answers with, so the
// agent can tell its user why and what to do instead.
func checkWriteErrorResult(err error) ToolCallResult {
	if errors.Is(err, checks.ErrDemoReadOnly) {
		return errorResult(auth.DemoWriteMessage)
	}

	return errorResult(err.Error())
}
