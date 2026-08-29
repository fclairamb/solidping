---
model: sonnet
effort: medium
---

# Consolidate Slack slash commands under `/solidping` (and make `/solidping` actually work)

## Problem

### 1. `/solidping` is registered with Slack but has no handler

Both manifests register `/solidping` with the usage hint
`[setup|create|list|config|incidents|ack|help]`
(`wiki/slack/manifest-prod.json`, `wiki/slack/manifest-dev.json`), and
<https://www.solidping.io/saas/slack> documents eight `/solidping` subcommands
in a table.

`DispatchCommand` (`server/internal/integrations/slack/commands.go:31-42`)
handles exactly two:

```go
switch cmd.Command {
case "/check":
	return dispatcher.handleCheckCommand(ctx, cmd)
case "/comment":
	return dispatcher.handleCommentCommand(ctx, cmd)
default:
	return &MessageResponse{
		ResponseType: ResponseTypeEphemeral,
		Text:         "Unknown command: " + cmd.Command,
	}, nil
}
```

There is no `case "/solidping"`, and no other dispatch path registers one —
`grep -rn 'case "/' server/internal/integrations/slack/` returns those two lines
and nothing else.

**So every documented `/solidping` subcommand answers `Unknown command:
/solidping` today.** `/solidping help`, the command Slack's own guidelines say
must return usage instructions, is among them. This is user-visible on every
install, it is advertised on the public landing page, and a Marketplace reviewer
will type it first.

### 2. `/check` and `/comment` are collision-prone global names

Slack's Marketplace guidelines ask for unique, namespaced slash-command names
(the suggested shape is `/[app_name]-help`) precisely because a slash command is
workspace-global. `/check` and `/comment` are about as generic as a command name
gets. A workspace that already has either — from another app or a custom
integration — gets a conflict at install time, and the installer has to pick
which app wins. Reviewers check for this.

The irony of the current state: the two commands that *do* work are the two that
should not exist, and the one that should be the only entry point does not work.

### 3. The routing logic already exists — it is just wired to the wrong transport

`handleMentionCommand` (`mention_commands.go:32-48`) is already a subcommand
router over `checks` / `results` / `incidents` / `config` / `help`, with a
sensible unknown-command fallback. `ParseMentionText` (`parser.go:31-`)
tokenizes respecting quotes, unwraps Slack's `<https://url|display>` link
formatting, extracts a subcommand for commands declared in `hasSubcommand`
(`parser.go:66-70`), and parses `-flag value` pairs.

That is exactly the parser `/solidping <subcommand>` needs. It is reachable only
from `app_mention` events today.

## Proposal

Make `/solidping` the single slash command, implemented by reusing the mention
parser, and retire the two generic names.

### 1. Route `/solidping` through the existing parser

In `DispatchCommand`, add a `/solidping` case that parses `cmd.Text` with the
mention parser and dispatches through the same subcommand router the
`app_mention` path uses. `ParseMentionText` strips a leading bot mention that
will not be present in slash-command text; on absent mention the regex replace is
a no-op, so the function is safe to call on both transports. Empty text already
maps to `cmdHelp` (`parser.go:41-45`), which gives `/solidping` → help for free.

The mention handlers post out-of-band via `chat.postMessage` and return `error`,
while the slash path returns `*MessageResponse`. Reconcile deliberately rather
than by accident: a slash command should answer **ephemerally** (guidelines:
"respond with ephemeral messages to minimize disruption"), where a mention
answers in-channel because the mention itself was public. Expect an adapter, not
a straight call — this is the bulk of the work in this spec.

Slack's 3-second ACK budget applies. Anything that can exceed it (check
creation, incident listing) must ACK immediately and follow up via
`response_url`, as `handleCheckCommand` effectively already arranges.

### 2. Cover the subcommands the manifest and landing page advertise

The hint promises `setup|create|list|config|incidents|ack|help`; the router
offers `checks|results|incidents|config|help`. Those two sets must be made to
agree — in whichever direction. Concretely:

| Advertised | Exists today | Resolution |
|---|---|---|
| `help` | `cmdHelp` | works once routed |
| `config` | `cmdConfig` | works once routed |
| `incidents` | `cmdIncidents` | works once routed |
| `list` | `checks list` | alias `list` → `checks list` |
| `create` | `checks add` | alias `create` → `checks add` |
| `ack` | — | needs a handler, or drop from the hint |
| `setup` | — | needs a handler, or drop from the hint |

**Do not ship a usage hint that names a subcommand with no handler.** Whatever is
not implemented in this change comes out of both manifests and out of the landing
page's command table in the same PR. The current mismatch is how we got here.

### 3. Retire `/check` and `/comment`

Remove both from `wiki/slack/manifest-prod.json` and
`wiki/slack/manifest-dev.json`, and re-point their behaviour:

- `/check <url>` → `/solidping check <url>` (keep `handleCheckCommand`'s URL
  normalization and validation as-is; only the entry point moves).
- `/comment [#42] <text>` → `/solidping comment [#42] <text>`.

`/comment`'s channel-scoped incident resolution is unaffected: it resolves from
the channel rather than from `thread_ts` because a slash payload carries no
`thread_ts` (`wiki/slack/README.md`), and that is still true under `/solidping`.

Keep the `case "/check"` and `case "/comment"` arms in `DispatchCommand` for at
least one release, answering ephemerally with "this command moved to
`/solidping check` — the standalone `/check` command is going away". A workspace
that installed the old manifest keeps its registered commands until it
re-authorizes, so deleting the arms strands those installs on `Unknown command`.

**Losing `/check https://example.com` has a real cost.** It is the nicest
affordance the integration has and it is the headline on the landing page.
The trade is: one less keystroke path against a Marketplace finding and a
genuine install-time conflict in any workspace that already defines `/check`.
This spec proposes making the trade; it is a product call, and if the answer is
"keep `/check`", then the fallback is to keep it *and* be ready to justify the
name in review.

### 4. Unknown input must produce usage

`DispatchCommand`'s `default` arm returns bare `"Unknown command: " + cmd.Command`
with no usage text (`commands.go:37-40`). Guidelines require unknown input to
respond with usage instructions. Mirror `handleMentionCommand`'s fallback
(`mention_commands.go:45-46`), which already points the user at `help`.

## Testing

- `/solidping` with empty text → ephemeral help.
- `/solidping help` → ephemeral help listing exactly the subcommands that exist.
- `/solidping <garbage>` → ephemeral unknown-command message that names `help`.
- `/solidping check https://example.com` and bare `example.com` (scheme added by
  `handleCheckCommand`) → check created.
- `/solidping check` with no argument → usage, not an error.
- `/solidping comment #42 text` and the ambiguous-incident path → unchanged
  behaviour under the new prefix.
- Legacy `/check` / `/comment` → the moved-command notice, ephemeral.
- Every subcommand named in the manifest usage hint has a dispatch case
  (worth a table-driven test that reads the hint, so the two cannot drift again —
  drift is the root cause of this spec).

## Out of scope

- Rendering real incidents in App Home (see the landing page's "see your active
  incidents at a glance" claim, which the static view does not honour).
- Adding `setup` and `ack` handlers, if the decision is to drop them from the
  hint instead.
- The `app_mention` transport's own behaviour, which is unchanged.

## Context

Found while assessing Slack Marketplace submission readiness (2026-08-29). The
eligibility gate for submission is 10+ active workspaces and 10+ weekly active
users, so there is time to do this properly — but a reviewer typing
`/solidping help` and getting `Unknown command` is a certain finding, and it is
a bad first five seconds for a real user too.
