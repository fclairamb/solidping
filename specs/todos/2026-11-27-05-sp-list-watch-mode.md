---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# `sp checks list` and `sp incidents list` have no live view

## Problem
Someone on a server shell who wants to keep an eye on check states or open incidents has to re-run `sp checks list --with-last-result` or `sp incidents list` by hand, or open dash0. A full TUI was considered and rejected: `sp` is mostly used by CI, scripts and agents, dash0 already covers interactive use, and the binary is kept small on purpose (`server/cmd/sp/deps_test.go`). A `--watch` flag on the two list commands covers the remaining case.

The server already pushes change hints on the org live socket `GET /api/v1/orgs/:org/events/ws` (`server/internal/app/server.go:1848`, handler `server/internal/handlers/realtimews/`). dash0 consumes it in `web/dash0/src/lib/live-socket.ts`: subscribe to a scope, receive `update` hints (no data, only which kind changed), refetch over REST, and refetch everything on `resync`.

## Proposal
1. Add a `--watch` bool flag to `checks list` (`server/pkg/cli/commands.go:170`, action `checksListAction`) and `incidents list` (`server/pkg/cli/commands.go:630`, action `incidentsListAction`). It combines with their existing flags (`--with-last-result`, `--internal`, `--all`, `--check`, ...).
2. `--watch` only works with table output. With `--output json` or `jsonl` (`server/pkg/cli/flags.go:31`) it fails at once with a validation error saying so.
3. A small live client in `server/pkg/cli/` (e.g. `live.go`) built on `github.com/coder/websocket` (already a direct dependency in `server/go.mod`). No TUI framework, no new dependency.
   - Connect to `/api/v1/orgs/<org>/events/ws` with the CLI's token, using the handshake `realtimews/handshake.go` accepts (subprotocol `solidping.v2`, plus the `bearer.*` subprotocol entry or an `Authorization` header).
   - Wait for `hello`, send `subscribe` for the collection scope (`checks` or `incidents`, no uid), wait for `subscribed`. Message types: `server/internal/handlers/realtimews/messages.go:11-21`.
   - On `update` or `resync`: refetch through the same REST call the list action already makes and redraw.
   - Debounce bursts of hints (e.g. 500 ms) so a flurry of results gives one refetch.
4. Redraw: clear the screen (ANSI `\x1b[H\x1b[2J`), print a header line (command, org, last refresh time, `live` or `polling`), then the table through the existing renderer (`server/pkg/cli/output/table.go:26`).
5. On disconnect, reconnect with exponential backoff (cap around 30 s) and refetch once reconnected. When the socket can't be used at all (handshake rejected, endpoint missing on an older server), fall back to polling every 10 s and show `polling` in the header.
6. Ctrl-C (SIGINT/SIGTERM) closes the socket and exits with code 0, through the command context.
7. Document `--watch` in the CLI reference under `web/docs/` next to the existing `sp` command docs, and add a CHANGELOG entry.

## Tests
- `server/pkg/cli/live_test.go` against an `httptest` server that speaks the protocol: subscribe is sent after `hello`; an `update` triggers one refetch; a `resync` triggers one refetch; several `update`s within the debounce window give one refetch.
- Same file: the server closes the socket, the client reconnects and refetches. Negative: the handshake answers 404/401, the client falls back to polling and the header says `polling`.
- `--watch` with `--output json` and `--output jsonl` returns the validation error and makes no network call.
- Context cancellation stops the loop and closes the socket (no goroutine leak, checked with the existing leak test helper if there is one).
- `server/cmd/sp/deps_test.go` still passes: none of the forbidden packages enter the `sp` graph.

## To verify
- Whether `realtimews/handshake.go` accepts an `Authorization: Bearer` header from a non-browser client, or only the cookie and `bearer.*` subprotocol. Use whichever it accepts, do not change the server's auth.
- How the CLI context exposes the base URL and token to build the `ws(s)://` URL (`server/pkg/cli/context.go`, `server/pkg/cli/apihelper/`).
- Where the `sp` command reference lives in `web/docs/`.
