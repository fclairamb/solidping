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

## Implementation Plan

### Confirmed root cause (read `urfave/cli/v3@v3.12.0` source directly, `go env GOMODCACHE`)

Both suspects in the Problem section are real and independent; either one alone would break
the repro, and they stack.

**Suspect 1 confirmed** — `flag_impl.go`'s `FlagBase.IsSet()` returns `f.hasBeenSet`, which is
only flipped to `true` when the value actually comes from a CLI arg / env / config source
(`flag_impl.go:154,213`), never for the flag's static `Value` default. So `cmd.String("org")`
being `!= ""` is a meaningless test when the flag's own `Value` default
(`defaults.Organization = "default"`) is non-empty — it's true on every invocation, flag passed
or not. `cmd.IsSet("org")` is the correct, and available, distinguishing check. Same story for
`--url` (`defaults.ServerURL = "http://localhost:4000"`, also non-empty).

**Suspect 2 confirmed, and it's the dominant bug** — `command.go`'s flag resolution:
- `Command.lookupFlag(name)` (`command.go:408`) walks `cmd.Lineage()` (self → parent →
  grandparent → …) and returns the **first** `Flag` object found by name — this is what
  `cmd.String()`/`cmd.IsSet()`/`cmd.Value()` all go through.
- `FlagBase.Local` (`flag_impl.go:66`, doc: "whether the flag needs to be applied to
  subcommands as well") defaults to `false` — i.e. **every flag is persistent/inherited by
  default** in urfave/cli v3, no `Persistent: true` needed. `command_parse.go:32-70`
  (`parseFlags`) walks every ancestor and copies each non-`Local` ancestor flag into the
  current command's `appliedFlags`, **but only if the current command doesn't already declare
  a flag of that same name itself** (`command_parse.go:56-58`: `if cmd.lFlag(name) != nil {
  applyPersistentFlag = false }`).
- `GetGlobalFlags()` (`pkg/cli/flags.go`) is a plain function returning a **freshly allocated**
  `[]cli.Flag` slice — a new `*cli.StringFlag{Name: "org", ...}` object — on every call. It is
  called once for the root command (`cmd/sp/main.go:24`) and again, independently, for nearly
  every command-group node (`auth`, `server`, `checks`, `results`, `incidents`, …, 28 call
  sites across `commands.go` plus one each in `orgs.go`, `files.go`, `params.go`,
  `email_suppressions.go`, `invitations.go`, `membership_requests.go`, `entitlements.go`, plus
  2 leaf commands — `checks diff` and `apply` — that do `append(GetGlobalFlags(), ...)`).
- Net effect: when a group node (e.g. `checks`) redeclares its own `org` flag object, that
  object — **unset, defaulted to `"default"`** — is what `cmd.lFlag("org")` finds on `checks`,
  which (a) blocks the root's parsed, actually-set `org` flag from propagating past `checks`
  in `appliedFlags`, and (b) is itself the object every descendant's `lookupFlag("org")`
  resolves to (it's nearer in `Lineage()` than root). A leaf like `checks list` doesn't
  redeclare `org` itself, so it finds `checks`'s shadow flag, not root's. This holds regardless
  of whether `--org test` was typed before or after `checks` on the command line — the shadow
  flag on `checks` is never the one urfave/cli actually parsed the value into.

So `cmd.IsSet("org")` alone is **not** sufficient — it would correctly say "not set" per the
above, but `cfg.Org` would still never receive an explicitly-passed `--org test` either, because
the value landed on the *root's* flag object, and a leaf command whose lineage passes through a
shadowing intermediate node never reaches it. The fix must remove the duplicate declarations,
not just patch the override check.

### Fix

1. `pkg/cli/context.go` (`NewCLIContext`): replace both non-empty-string checks with
   `cmd.IsSet(...)`:
   - `if cmd.IsSet(flagURL) { cfg.URL = cmd.String(flagURL) }`
   - `if cmd.IsSet("org") { cfg.Org = cmd.String("org") }`
2. Make `GetGlobalFlags()` single-source: keep the one call in `cmd/sp/main.go` (root
   `Command.Flags`), and delete every other call site so no node in the tree ever redeclares
   `config`/`url`/`org`/`output`/`json`/`verbose`. Persistence is the v3 default (`Local:
   false`), so removing the shadow declarations is enough — no `Persistent: true` needed.
   - `commands.go`: remove `Flags: GetGlobalFlags(),` from the 26 group-level `*cli.Command`
     literals that have it (`auth`, `server`, `checks`, `results`, `incidents`, `channels`,
     `events`, `tokens`, `members`, `jobs`, `check-jobs`, `system`, `discovery`, `heartbeat`,
     `status-pages`, `status-updates`, `maintenance-windows`, `check-groups`, `severities`,
     `labels`, `regions`, `check-types`, `oncall`, `notifications`, `notification-routes`,
     `notification-contacts`, `escalation-policies`); change the 2
     `append(GetGlobalFlags(), <extra>...)` leaf sites (`checks diff`, `apply`) to just
     `<extra>` (a plain `[]cli.Flag{...}` of their own flags).
   - `orgs.go`, `files.go`, `params.go`, `email_suppressions.go`, `invitations.go`,
     `membership_requests.go`, `entitlements.go`: remove their single
     `Flags: GetGlobalFlags(),` line each.
3. Run `make fmt` after each mechanical removal pass to fix struct-literal alignment.

### Tests (`pkg/cli/context_test.go`, new file)

1. `TestNewCLIContext_ConfigOrgUsedWhenFlagNotPassed` — write a temp config file with
   `"org": "acme"`, build a `*cli.Command` with `GetGlobalFlags()` and no args beyond
   `--config <path>`, call `NewCLIContext` directly, assert `cfg.Config.Org == "acme"`.
2. `TestNewCLIContext_DefaultOrgWhenNothingSet` — temp config file with no `org` key (or a
   nonexistent config path), assert `cfg.Config.Org == defaults.Organization` (`"default"`).
3. `TestNewCLIContext_ExplicitFlagOverridesConfig` — temp config file with `"org": "acme"`,
   parse `--config <path> --org test`, assert `cfg.Config.Org == "test"`.
4. `TestCLI_OrgFlagPropagatesThroughCommandTree` (end-to-end) — build a small 3-level command
   tree (`root` → group → leaf, mirroring the real shape) with `GetGlobalFlags()` declared
   **only** on `root`, an `Action` on the leaf that calls `NewCLIContext(cmd)` and stashes the
   resulting `Org` in a captured variable; run `root.Run(ctx, []string{"root", "--org", "test",
   "group", "leaf"})`; assert the captured org is `"test"`. Add a second run of the same tree
   with the flag redeclared on the intermediate `group` node (reproducing the pre-fix shape) to
   document the regression this guards against, asserting it would NOT see `"test"` — proving
   the test actually exercises the propagation mechanism and isn't vacuously true.

### Addendum — a second, unaudited call site (`server/main.go`), found by the coordinator's audit

The enumeration above only covered `pkg/cli/*.go`. `pkg/cli` is also reused by a **second**
entry point: the `solidping` server binary's own command tree (`server/main.go`) registers a
`client` node — `solidping client <cmd>` — whose `Flags` was `spCli.GetGlobalFlags()` and whose
`Commands` was `spCli.GetCommands()`. This is architecturally distinct from `cmd/sp` in one way
that matters: the `solidping` root command sets `DefaultCommand: "serve"` and, pre-fix, declared
no `Flags` of its own.

That combination reproduces a **different** failure mode from the shadowing bug above — not a
same-named flag shadowing an ancestor's, but v3's explicit `DefaultCommand` fallback
(`command_parse.go:206-210`, fixing upstream issue #2249): when a command with `DefaultCommand`
set encounters a flag it doesn't recognize *before* the first subcommand name, v3 passes that
flag and everything after it through as **positional arguments** rather than erroring or
continuing to look for a subcommand. Since the `solidping` root declared no flags of its own,
`solidping --org test client checks list` never even reached the `client` node's flags — `--org`,
`test`, `client`, `checks`, `list` all became positional args handed to the default `serve`
command. `solidping client --org test checks list` (flag at-or-after `client`) worked correctly,
because by the time parsing reaches `client`, `client` itself declared the flags directly and
there's no unrecognized-flag/`DefaultCommand` interaction at that level.

**Fix**: apply the exact same single-declaration principle one level up — move
`Flags: spCli.GetGlobalFlags()` from the `client` node to the `solidping` root `Command` in
`server/main.go`, and remove it from `client`. The root already accepts subcommands
(`serve`, `healthcheck`, `migrate`, `encrypt-credentials`, `dev`, `client`); none of those
declare a flag named `config`/`url`/`org`/`output`/`json`/`verbose`, so there's no shadowing
risk, and v3's default persistence (`FlagBase.Local == false`) carries the root's now-recognized
`--org`/`--url`/etc. down through `client` and every `pkg/cli` node beneath it, exactly as it
does for `cmd/sp`. Verified manually (`go run .` against a disposable config, no live server
needed — the outbound login request body shows the resolved org) in all three flag positions:
before `client`, right after `client`, and after the leaf command. The command-tree
construction inline in `func main()` was extracted into a side-effect-free
`buildRootCommand()`, called by both `main()` and `server/main_test.go`, so the
`TestClientOrgFlag_*` regression tests walk down to the real `client` → `checks` → `list`
leaf and swap out only that leaf's `Action` (to capture the resolved org without a network
call) — every `Flags`/`Commands`/`DefaultCommand` on the path is exactly what production
ships, not a hand-fabricated mirror. Verified non-vacuous by temporarily reintroducing the
bug (redeclaring `Flags` on `client`) and confirming `TestClientOrgFlag_BeforeClient` fails,
then reverting.

This means the fix commit's "no node in the tree redeclares config/url/org/output/json/verbose"
claim, as originally written, was accurate only for the `cmd/sp` binary's command tree, not for
`solidping client`'s. The `server/main.go` change closes that gap; both entry points now
single-source these flags at their own respective root.
