---
model: sonnet
effort: medium
---

# The Discord DM tests race on one package-level client factory, so the happy path randomly talks to another test's fake server

## Problem

`TestSendDiscordDMOpensCachesAndPosts`
(`server/internal/opsnotifywire/discord_test.go:113`) failed twice during full
`make test` runs on 2026-09-22, each time with a Discord API error code the happy
path never stubs:

- `code 50007: Cannot send messages to this user`
- `code 50013: Missing Permissions`

Both codes are the fixtures of a *different* test in the same file,
`TestSendDiscordDM50007IsUnavailableAnd5xxIsNot`
(`server/internal/opsnotifywire/discord_test.go:157`). Run in isolation
(`go test ./internal/opsnotifywire/... -run TestSendDiscordDMOpensCachesAndPosts
-count=5 -short`) the happy path passes every time, because nothing else is
mutating the seam.

The nondeterminism is not in the mock — it is the seam itself. The stand-in is
installed by writing a **package-level variable**:

- `server/internal/opsnotifywire/wire.go:218` — `var newDiscordBotClient = discord.NewBotClient`
- `server/internal/opsnotifywire/export_test.go:7` — `SetDiscordBotClientFactory` saves the previous value, overwrites the global, and returns a restore func
- `server/internal/opsnotifywire/discord_test.go:70` — `fakeDiscordDM.install(t)` registers that restore via `t.Cleanup`
- `server/internal/opsnotifywire/wire.go:196` — `sendDiscordDM` **reads** the global on every send, not at `Build` time

Every test in the file calls `t.Parallel()`, and so do the three subtests of the
error-code table (`discord_test.go:182`). That means four goroutines concurrently
write and read one variable:

1. The happy path installs its own fake and starts sending.
2. A `50007` / `50013` subtest installs its fake over the top.
3. The happy path's second `SendDiscordDM` re-reads the global and posts to the
   *error* server — which answers `403 {"code":50007,...}` or
   `403 {"code":50013,...}`. Exactly the two observed failures.

Two further consequences of the same defect, worth fixing in the same pass:

- **The restore is wrong under interleaving.** Each `install` captures whatever
  value happened to be current, so a `t.Cleanup` can restore a factory pointing
  at an `httptest.Server` that its own cleanup has already closed. A later test
  then gets connection-refused rather than a wrong-code failure — a third failure
  shape from one bug.
- **It is a genuine data race** on `newDiscordBotClient`, so `go test -race` on
  this package is unsound today even when the assertions happen to pass.

Per the repo convention (`feedback_flaky_tests_are_bugs`), this is a bug in the
test seam, not a flake to re-run.

## Proposal

Remove the mutable global so each test owns its own client, and the seam stops
being process-wide state.

**1. Make the factory per-`Build` instead of per-package.**

`sendDiscordDM(dbSvc, cfg)` should close over the factory it was given rather
than reading a package variable on each call. Concretely: thread an optional
bot-client factory through `Build` (an options struct or a variadic option, so
the three production callers — `server/internal/app/opsnotify_wiring.go:58`,
`server/internal/jobs/jobtypes/job_platform_watchdog_delivery.go:64`, and the
existing tests — keep working with the real `discord.NewBotClient` default).
Delete `newDiscordBotClient` and `SetDiscordBotClientFactory` once nothing reads
them; if `export_test.go` ends up empty, delete the file too.

Resolve the factory **once** when `Build` constructs the closure, not on every
send, so a single `opsnotify.Deps` can never change which server it talks to
mid-test.

An acceptable alternative if threading an option through `Build` proves noisy:
give `config.DiscordOAuthConfig` an API base-URL override and have
`sendDiscordDM` apply it via `discord.BotClient.WithBaseURL`. Each test already
builds its own `*config.Config` (`discordEnv` / `discordBotConfig()`), so that is
also per-test state with no global. Pick one; do not keep both.

**2. Keep `t.Parallel()` everywhere.**

Dropping `t.Parallel()` would make the symptom disappear while leaving the shared
mutable seam in place for the next test that touches it. The tests must stay
parallel, and pass because they are actually independent.

**3. Prove it, including the negative.**

- `go test ./internal/opsnotifywire/... -race -count=20 -short` must be clean.
  `-race` is the load-bearing flag here: it fails on the *current* code and must
  pass after, which is the positive control that the race is gone rather than
  merely less likely.
- Assert the isolation directly: the happy-path test should still see
  `fake.dmOpened == 1` and `fake.posted == 2` on **its own** fake while the
  error-table subtests run, i.e. each `fakeDiscordDM`'s counters may only be
  moved by the `Deps` built alongside it.
- Check whether `server/internal/integrations/discord/service_install_test.go:57`
  swaps a factory the same way. It is a different package-level variable in a
  different package, so it is not the cause of *this* failure, but if it is also
  a global mutated from parallel tests it carries the same latent bug and should
  be converted in the same change.
