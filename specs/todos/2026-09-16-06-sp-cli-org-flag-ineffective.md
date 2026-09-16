---
model: sonnet
effort: high
---

# `sp` CLI ignores both `--org` and the config file's `Org`, always hitting org "default"

## Problem

While implementing and smoke-testing `sp checks import --from uptime-kuma-db` (spec
`specs/todos/2026-09-15-09-sp-import-from-uptime-kuma-db.md`) against a live `make dev-test`
server, every `sp` request goes to org `default` regardless of configuration or flags.

Reproduction, confirmed on a pristine worktree at commit `4b92e6b9b` (before any import-command
changes, so this is pre-existing and unrelated to that spec):

```bash
cd server
# ~/.config/solidping/settings.json has "Org": "test"
go run ./cmd/sp checks list             # hits /api/v1/orgs/default/checks (seen in backend.log)
go run ./cmd/sp --org test checks list  # STILL hits /api/v1/orgs/default/checks
```

Direct `curl` with the same bearer token against `/api/v1/orgs/test/checks` succeeds, so org
`test` resolves correctly server-side — this is a CLI-side bug only.

### Suspect 1 — the override check in `NewCLIContext` can never see "not set"

[server/pkg/cli/context.go:60-62](server/pkg/cli/context.go#L60-L62):

```go
// Override org if provided via flag
if org := cmd.String("org"); org != "" {
    cfg.Org = org
}
```

The `--org` flag is declared with a non-empty default
([server/pkg/cli/flags.go:24-29](server/pkg/cli/flags.go#L24-L29)):

```go
&cli.StringFlag{
    Name:    "org",
    Usage:   "Organization name (overrides config value)",
    Value:   defaults.Organization,   // "default"
    Sources: cli.EnvVars("SOLIDPING_ORG"),
},
```

Because the flag's own default is the string `"default"`, `cmd.String("org")` is never `""` —
so this condition unconditionally overwrites `cfg.Org`, including on every invocation where
the user did **not** pass `--org` at all. That alone explains the config-file case.

### Suspect 2 — an explicit `--org test` before the subcommand is still lost

That does not explain why passing `--org test` explicitly also has no effect. `GetGlobalFlags()`
([server/pkg/cli/flags.go:10-49](server/pkg/cli/flags.go#L10-L49)) is a plain function that
allocates and returns a **new** slice of flag objects on every call, and it is called
separately for the root command (`server/cmd/sp/main.go:24`, `Flags: cli.GetGlobalFlags()`)
*and* for essentially every leaf subcommand (dozens of call sites across
`server/pkg/cli/commands.go`, `orgs.go`, `params.go`, `email_suppressions.go`,
`invitations.go`, `files.go`, `membership_requests.go`, `entitlements.go`, each with their own
`Flags: GetGlobalFlags()`). Each of these is an independently-instantiated `*cli.StringFlag`
for `"org"`, not a shared reference.

Hypothesis: with urfave/cli v3, a flag parsed at the root level (`sp --org test checks list`)
does not automatically populate a same-named flag re-declared on a leaf subcommand — the leaf's
own flag instance never gets `Set`, so inside the leaf's `Action`, `cmd.String("org")` reads the
leaf's own unset flag (falling back to its default `"default"`), not the value the root parsed.
This needs to be confirmed by reading `urfave/cli/v3`'s flag/context resolution (in particular
whether it treats same-named flags across a command tree as one persistent value, and whether
`cmd.String` walks up to a parent command's parsed flags) — but it is consistent with every
symptom observed, including that a **global**, not per-command, flag position is used
everywhere in the reproduction.

## Proposal

1. Investigate `urfave/cli/v3` flag semantics to confirm the mechanism above:
   - Does `cmd.IsSet("org")` correctly distinguish "flag not passed" from "flag passed with a
     value equal to the default", regardless of where the flag is declared?
   - Does a flag need to be marked `Persistent: true` (or declared once on the root command
     only, inherited by children) to propagate from `sp --org test <subcommand>` down into the
     subcommand's `Action`? Check whether `GetGlobalFlags()` being called independently per
     subcommand is itself the bug (each call producing a disconnected flag instance), versus a
     more fundamental v3 inheritance behavior.
2. Fix `NewCLIContext` in [server/pkg/cli/context.go](server/pkg/cli/context.go) so that:
   - Config-file `Org` is used when `--org` was not explicitly passed (use `cmd.IsSet("org")`
     rather than a non-empty-string check, once confirmed reliable).
   - An explicitly-passed `--org` (in any valid position — before or after the subcommand, per
     whatever urfave/cli v3 supports) always wins over the config file.
   - The same treatment should apply to `--url` if it has the same non-empty-default pitfall
     ([server/pkg/cli/flags.go:18-23](server/pkg/cli/flags.go#L18-L23) — check whether config
     file values for the server URL are similarly being clobbered).
3. If the root cause is duplicate flag declarations across the command tree, consider
   whether `GetGlobalFlags()` needs restructuring (e.g. declared once, marked persistent, and
   not redeclared per leaf command) rather than patched around in `NewCLIContext` alone.
4. Add regression tests covering:
   - No `--org` flag, config file `Org` set to a non-default value → that org is used.
   - No `--org` flag, no config file `Org` → falls back to `"default"`.
   - Explicit `--org test` (whatever position urfave/cli v3 supports) with a config file `Org`
     set to something else → `--org` wins.
   - Ideally an end-to-end-ish test (not just unit-testing `NewCLIContext` in isolation) that
     exercises actual CLI argument parsing through a subcommand's `Action`, since the bug is in
     the interaction between root/leaf flag declarations, not just the override logic.

This affects every `sp` subcommand that talks to a non-`"default"` org — every user of a
multi-org instance, or anyone whose org isn't literally named `default`, is currently unable to
target their org via `sp` at all. Worth a dedicated, careful fix rather than a quick patch.
