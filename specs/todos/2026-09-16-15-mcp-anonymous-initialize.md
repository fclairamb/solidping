# Allow anonymous `initialize` on the MCP endpoint

## Why

MCP directories probe a server by starting it and sending the `initialize`
handshake. Ours answers `401 NO_TOKEN`, because `POST /api/v1/mcp` sits behind
`RequireMCPAuth` (`server/internal/app/server.go:977`). That is correct for
every real call and wrong for discovery, and it currently blocks a listing.

Concretely: [punkpeye/awesome-mcp-servers#14422](https://github.com/punkpeye/awesome-mcp-servers/pull/14422)
cannot merge until solidping is listed on Glama, and the maintainer confirmed on
2026-09-15 that HTTP transport is fine but *"if authentication is required for
the MCP endpoint, that may cause the Glama check to fail because anonymous
introspection won't work"*. Their two suggested ways out are to hand Glama an
auth token or to allow unauthenticated introspection.

Handing a third-party directory a bearer token for the hosted MCP endpoint is
the worse option: it is a real credential, it would live in someone else's
config, and it buys one listing. Answering `initialize` anonymously buys every
directory, forever, and leaks nothing that is not already public.

## What

Let `initialize` (and only `initialize`) through before `RequireMCPAuth`.

- `initialize` already returns `ServerInfo{Name: "solidping", Version: ...}`
  plus the protocol version and capability flags
  (`server/internal/mcp/handler.go:377`). All three are public facts.
- Every other method stays authenticated. In particular `tools/list`,
  `resources/list` and any `tools/call` must still 401 without a token: the
  tool surface is a fingerprint of the product and the calls touch org data.
- `notifications/initialized` should also be accepted anonymously if the
  handshake requires it, since it carries nothing.

## Acceptance

- `POST /api/v1/mcp` with an `initialize` request and no `Authorization` header
  returns a valid JSON-RPC result, not 401.
- The same request with `method: tools/list` still returns 401 `NO_TOKEN`.
- An authenticated session is unaffected: initialize → tools/list → tools/call
  works exactly as before.
- A test covers the anonymous-initialize path and the still-401 path, next to
  the existing tests in `server/internal/mcp/`.

## Out of scope

Anything that would expose the tool list, resources, or org data without a
token. If a directory demands `tools/list` anonymously, that is a separate
decision and the answer is probably no.

## Origin

Filed by the business agent from the awesome-mcp-servers submission
(`solidping-business/memory/publications.md`, item 6). Business context: the one
substantive piece of user feedback from the r/selfhosted thread was that the API
and MCP were sufficient and the UI was barely used, so the MCP surface is worth
making discoverable.
