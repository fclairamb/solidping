---
model: opus
effort: medium
---

# A fresh instance idles under 128 MiB RSS, and a password hash does not leave 64 MiB behind

## Problem

Right after start, SolidPing v0.38.3 used about 270 MB RSS on arm64 (Debian, SQLite, fresh database, single node). This was seen while packaging it for DietPi. Many self-hosters run it on 1 GB Raspberry Pis and small VPSes, and tools in this category get compared on footprint.

### What was measured

`ghcr.io/fclairamb/solidping:0.38.3`, linux/arm64, Docker Desktop on a Mac, fresh named volume, no limits. Readings come from the container's cgroup (`memory.stat`) and from `/proc/<pid>/status`, read from a privileged helper container. A second container ran with `SP_PROFILER_ENABLED=true` for pprof, and the floor was read from `GET /api/mgmt/memory?gc=1`.

| Moment | VmRSS | RssAnon | RssFile | VmHWM |
|---|---:|---:|---:|---:|
| Fresh DB, ~60-80 s after start | 229-244 MB | 145-157 MB | 75-87 MB | 229-245 MB |
| Fresh DB, 3 min after start | 111 MB | 41 MB | 70 MB | 238 MB |
| Existing DB (restart), 29 s after start | 107 MB | 32 MB | 75 MB | **107 MB** |
| After one password login (2 s later) | 181 MB | 106 MB | 76 MB | |
| After login + forced password change | 239 MB | 163 MB | 76 MB | **309 MB** |
| Same, right after `debug.FreeOSMemory()` (`?gc=1`) | 113 MB | **37 MB** | 76 MB | |

Other facts from the same runs:

- Live heap after a forced GC: 9-11 MB. Goroutines: 61-66. Goroutine stacks: 3 MB. OS threads: 13-16.
- `GOMEMLIMIT` / `GOGC` at boot: unlimited / 100 (log line `Runtime memory guardrails applied memoryLimit=unlimited memoryLimitSource=default`). The cgroup auto-cap is a no-op without a container memory limit, which is the DietPi case.
- `docker stats` is misleading here. The first container that read the image showed **287 MiB at 23 s**, because ~160 MiB of page cache for the 268 MB binary was charged to its cgroup. A second container on the same image showed 130 MiB at the same age. Use VmRSS / RssAnon, not `docker stats`.
- `docker stats` on a clean fresh container: 147.9 MiB at 1 min, 48.3 MiB at 3 min.

So the reported ~270 MB is the first minutes of a first boot: about 150 MB of anonymous memory plus about 80 MB of file-backed binary pages.

### Root cause: argon2id leaves its 64 MiB block behind, and inflates the GC goal

The alloc_space profile at ~2 min on a fresh instance: **64 MB of 165 MB total allocations come from `golang.org/x/crypto/argon2.initBlocks`**, called from `StartupJobRun.ensureDefaultOrganization` -> `passwords.Hash` (`server/internal/jobs/jobtypes/job_startup.go:341-343`). The seeded admin's well-known password `defaults.Password` is hashed with the default argon2id profile, `m=64 MiB` (`server/internal/config/config.go:1968`, `server/internal/utils/passwords/policy.go:44`), through `boundedIDKey` (`server/internal/utils/passwords/argon2id.go:50-55`).

A GC runs while that 64 MiB block is live. Live heap is then ~75 MB, so with `GOGC=100` the next GC goal becomes ~150 MB (`NextGC = 150037274` read from `/debug/pprof/heap?debug=1`). An idle instance allocates about 0.4 MB/s, so the heap grows toward 150 MB and stays resident for 1 to 3 minutes before the next GC and the background scavenger give it back.

The same thing happens on every password verify: one login took RssAnon from 40 to 106 MB in 2 s, to 135 MB 90 s later, with `NextGC = 153575050`. A forced password change (verify + new hash) pushed VmHWM to 309 MB. A plain `runtime.GC()` reset the goal (to ~20 MB) but did **not** lower RSS right away (still 135 MB). `debug.FreeOSMemory()` did: 163 -> 37 MB at once.

Confirmation by experiment, three fresh volumes started side by side, read at ~90 s:

| Variant | RssAnon at ~90 s | VmHWM |
|---|---:|---:|
| default | 145 MB (at 63 s) | 229-245 MB |
| `SP_AUTH_PASSWORD_ARGON2_MEMORY=19456` | 37 MB | 151 MB |
| `GOMEMLIMIT=96MiB` | 35 MB | 169 MB |
| `GOGC=50` | 34 MB | 207 MB |

And a restart on an existing database (no seed hash) peaks at 107 MB.

This also corrects the memory runbook. `wiki/runbooks/memory-profiling.md:310` attributes the 148.6 MiB "startup burst" of `idle-all-sqlite` to "Boot (migrations, seeding, config)". It is the seed admin's argon2 hash, plus the bench's own login (`server/cmd/membench/client.go:71`, which verifies the same 64 MiB hash).

### Smaller findings

- **Regexp compiled on every call.** `parsePostgresInterval` (`server/internal/utils/timeutils/iso8601.go:191`) calls `regexp.MustCompile` each time. `Duration.Scan` (`:150`) falls back to it, and the check claim path scans `period` for every upcoming job in `nextEligibleIn` (`server/internal/checkworker/checkjobsvc/service.go:864`, called at `:322`, `:572`, `:643`). That is **32 MB of the 165 MB** allocated in the first 2 minutes (about 20%): pure garbage and GC work, every fetch, forever.
- **Browser sample checks run on every fresh instance.** The startup job creates 70 sample checks, all `Enabled = true` (`job_startup.go:514`). Two need a browser: `js-browser-login` (`server/internal/checkers/checkjs/samples.go:163-170`, every **1 minute**, against `https://app.example.com`, which cannot pass) and `browser-example-com` (`server/internal/checkers/checkbrowser/samples.go:14`, hourly). In the Docker image there is no Chrome (distroless, PIDs stayed at 13-16), so they fail fast and cost nothing. On a native install where Chromium is present, the exec allocator (`checkbrowser/checker.go:476-495`) starts a fresh Chrome for each run, so once a minute. Not measured here. The runbook's cluster figures put a headless Chrome at 38-119 Mi.

### Checked, and not a cause

- **Headless browser.** Not started at boot. Each run opens its own session (`checkbrowser/session.go:141`, `openSessionPinned` at `:160`). No Chrome process was seen.
- **JS runtime.** No pool. `goja.New()` is called once per execution (`server/internal/checkers/checkjs/checker.go:345`). No `sync.Pool` anywhere under `server/internal`.
- **Embedded SPAs and docs.** Streamed from the `embed.FS` (`server/internal/app/embedded_assets.go:59`, `writeEmbeddedFile`). Only `dash0res/index.html` is read once into memory (`server/internal/app/security_headers.go:184`). Not held in decoded form.
- **Worker pools.** 25 check runners (`config.go:1887`) and 2 job runners. All 61-66 goroutines together use 3 MB of stack.
- **SQLite.** One connection (`server/internal/db/sqlite/sqlite.go:170`), default page cache. Driver is pure-Go modernc, so its memory is on the Go heap, which is 9-11 MB live in total.
- **Binary size.** The binary is 268 MB: `.text` 58 MiB, `.rodata` 68 MiB, `.gopclntab` 51 MiB, `.go.type` 16 MiB, plus ~50 MB of DWARF, `.symtab` and `.strtab`. Stripping (`-s -w`) would shrink the image but not RSS, since those sections are not loaded. The 70-85 MB RssFile is binary pages touched at init (package `init` work from k8s, protobuf and similar registries shows in the heap profile). Those pages are clean and reclaimable, but `top` and `ps` count them. Trimming them means trimming dependencies and is out of scope here.

## Proposal

Ranked by expected saving, with the number each one is based on.

1. **Give argon2 memory back after a burst of hashes** (expected: RssAnon ~150 -> ~35-40 MB on a fresh instance at 60 s; ~106-163 -> ~37 MB after a login or password change). In `server/internal/utils/passwords/argon2id.go`, after `boundedIDKey` releases its `hashSem` slot, schedule one memory release when no other derivation is in flight:
   - Track in-flight derivations (an `atomic.Int32` next to `hashSem`, or `len(hashSem)` after the release).
   - When the count drops to zero, (re)arm a single `time.AfterFunc(releaseDelay, release)`. A new derivation that starts before it fires keeps the timer from releasing in the middle of a burst. The timer fires once after the last one ends.
   - `release` calls `debug.FreeOSMemory()`. Measured: it took RssAnon from 163 to 37 MB at once. A plain `runtime.GC()` did not, so do not substitute it.
   - `releaseDelay` defaults to 1 s. Both `releaseDelay` and `release` are package variables so tests can inject them.
   - Only argon2id schedules a release. bcrypt uses about 4 KiB and does not.
   - Cost: one full GC on a 9-11 MB live heap per login burst. Logins are rare. No config knob is needed. If an implementer wants one, use `SP_AUTH_PASSWORD_RELEASE_MEMORY` (default `true`).
2. **Seed the bootstrap admin without argon2** (expected: first-boot VmHWM 229-245 MB -> about 107 MB, the measured peak of a boot that does no hash). In `ensureDefaultOrganization` (`job_startup.go:341-343`), hash `defaults.Password` with bcrypt (`hashBcrypt`, `server/internal/utils/passwords/bcrypt.go:45`) and the configured bcrypt cost, instead of `passwords.Hash`. Add an exported helper, e.g. `passwords.HashBcrypt(password string) (string, error)`, rather than calling the unexported one from the job package. Why this is safe:
   - The password is public anyway. The account is protected by `MustChangePassword = true` (`:357`), which is unchanged.
   - `Verify` dispatches on the stored hash marker (`passwords.go:36`), so the bcrypt hash verifies.
   - The forced password change mints a fresh argon2id hash with the active policy. `RehashOnLogin` (`config.go:1286`) also covers it.
   - The first login still costs one 64 MiB derivation (the new password), which step 1 gives back.
   - Existing installs are untouched: the seed only runs when there is no organization.
3. **Create browser-dependent sample checks disabled** (expected: removes one Chrome launch per minute on native installs that have Chromium; 0 in the Docker image). In `createSampleCheck` (`job_startup.go:497`), set `Enabled = false` for samples whose type is `browser`, and for the `js` sample `sampleBrowserLoginSlug`. Prefer a field on `checkerdef.CheckSpec` (e.g. `Disabled bool`, or `NeedsBrowser bool`) set in the two sample definitions, over a slug check in the job. The samples still exist and show the feature. The user enables them when they point them at a real site. Do not gate on `checkbrowser.Available` (`availability.go:117`): even with Chrome present, `app.example.com` can never pass.
4. **Compile the interval regexp once** (expected: about 20% fewer bytes allocated on an idle instance, so fewer GC cycles and less CPU on a Pi; RSS effect small). Move the pattern in `parsePostgresInterval` (`iso8601.go:191`) to a package-level `var postgresIntervalRe = regexp.MustCompile(...)`. Same behaviour.
5. **Document a low-memory profile.** Add a "Small hosts (1 GB and less)" section to `web/docs/docs/installation/linux.md` and link it from `web/docs/docs/installation/docker.md`. It lists, with the measured effect of each:
   - `SP_RUNTIME_MEMORY_LIMIT=128MiB` (or native `GOMEMLIMIT`). Measured at 96 MiB: VmHWM 169 MB instead of 229-245 MB. Explain that it is a soft cap.
   - `SP_SERVER_CHECK_WORKER_NB` only needs lowering if the host runs many slow checks at once. Its idle memory cost is negligible.
   - Optional, with a clear security note: `SP_AUTH_PASSWORD_ARGON2_MEMORY=19456`, `SP_AUTH_PASSWORD_ARGON2_TIME=2`, `SP_AUTH_PASSWORD_ARGON2_THREADS=1` (the OWASP minimum profile), which caps each login's transient at 19 MiB.
   - That `ps`/`top` RSS includes 70-85 MB of shared, reclaimable binary pages, and RssAnon is the number that matters.
   - Also document the `SP_RUNTIME_*` knobs in `web/docs/docs/configuration/index.md`. They are only in the wiki today (`wiki/runbooks/memory-profiling.md`, Appendix B).
   - Fix the drift found while checking: `web/docs/docs/configuration/index.md:59` and `wiki/conventions/runners.md:141` say `SP_SERVER_CHECK_WORKER_NB` defaults to `3`. The code default is `25` (`config.go:1887`).
6. **Host-RAM soft cap as a guardrail on small hosts** (expected idle saving after steps 1-2: none; it bounds bursts). In `server/internal/memlimit/memlimit.go`, when `Auto` is on, there is no cgroup limit (`memLimitFromCgroup`, `:157`), no config and no env var, read `MemTotal` from `/proc/meminfo`. If it is at most 2 GiB, set the soft cap to `MemTotal / 2`, with source `"host"`. Put the reader behind the same injected-detector pattern as `detectCgroupMemoryLimitOS` (`cgroup_linux.go:27`, `cgroup_other.go:8`) so it is testable and a no-op off Linux. It is logged by the existing `Runtime memory guardrails applied` line (`server/main.go:251-262`). On a 1 GB Pi the cap is 512 MiB, far above an idle instance, so it only matters during login or check bursts. If the implementer judges the risk higher than this benefit, skip it and record why in the spec. It is last on purpose.
7. **Correct the runbook.** In `wiki/runbooks/memory-profiling.md` §5 (`:286-330`), replace the "Boot (migrations, seeding, config)" explanation with the argon2 one above. Note that membench's own login adds a second 64 MiB derivation. Add the before/after numbers of this spec to §9 as a new ranked row.

Backward compatibility: no config key changes meaning. Existing password hashes, existing sample checks and existing installs are untouched. Step 6 only applies when nothing else set a cap, and `SP_RUNTIME_AUTO_MEMORY_LIMIT=false` turns it off as it does today.

Target: on a fresh instance (empty volume, linux/arm64 image), **60 s after start: RssAnon at most 48 MiB and VmRSS at most 128 MiB** (today 145-157 MB and 229-244 MB). **10 s after a password login: RssAnon at most 48 MiB** (today 106 MB, rising to 135 MB). **First-boot VmHWM at most 128 MiB** (today 229-245 MB).

## Tests

Table-driven Go tests per `server/AGENTS.md`.

- `server/internal/utils/passwords/argon2id_test.go`, `TestReleaseScheduling` (fake `release` counter, `releaseDelay` set to a few ms):

  | case | expected releases |
  |---|---|
  | one argon2id `Hash` | 1, after the hash returns |
  | one argon2id `Verify` | 1 |
  | 4 overlapping hashes | 1, after the last one ends |
  | a hash that starts while the timer is armed | still 1, fired after the second hash |
  | one bcrypt hash | 0 |

- `server/internal/utils/passwords/memrelease_test.go`, `TestArgon2MemoryIsReturned`: re-exec the test binary in a child process (`os.Args[0] -test.run=...` with an env flag) so other tests' heaps do not interfere. In the child, read the runtime/metrics baseline: retained heap = `/memory/classes/total:bytes` minus `/memory/classes/heap/released:bytes`, plus `/gc/heap/goal:bytes`. Run `Hash` with the default 64 MiB profile, wait `releaseDelay` plus a margin, and do **not** call `runtime.GC` from the test. Assert retained heap is at most baseline + 16 MiB and the heap goal is at most 48 MiB. Tolerance is set against a 64 MiB signal. Log the numbers with `t.Logf`.
- `server/internal/jobs/jobtypes` (startup job test file): the seeded admin's stored hash has the bcrypt marker. `passwords.Verify(defaults.Password, hash)` is true, `passwords.ShouldRehash(hash)` is true under the default argon2id policy, and `MustChangePassword` is still true.
- Startup memory guard, `server/internal/app` (or wherever a full `NewServer` + `Initialize` + startup job can run on `sqlite-memory`), `TestFreshBootHeapStaysSmall`: child process again. Run the startup job end to end, wait `releaseDelay` plus a margin, no forced GC. Assert `/gc/heap/goal:bytes` is at most 64 MiB and retained heap is at most 64 MiB. Today this test would see a goal of ~150 MB. Log the values so a drift is visible before it fails.
- Sample checks: after the startup job, `browser-example-com` and `js-browser-login` exist with `Enabled = false`. A control sample (e.g. `http-test-api`) is enabled.
- `server/internal/utils/timeutils`: a table test that `Duration.Scan` parses `"00:05:00"`, `"01:00:00.5"` and `"100:00:00"` the same as before, and rejects `"5:00"`. Add `BenchmarkDurationScanInterval` with `b.ReportAllocs()` to show the per-call regexp compile is gone.
- `server/internal/memlimit/memlimit_test.go`, `TestDecideMemoryLimit` (`:16`), new rows with an injected host detector: 1 GiB host with no cgroup gives a 512 MiB cap from source `host`; an 8 GiB host gives none; a cgroup limit wins over host; config wins over host; `GOMEMLIMIT` env wins over everything; `Auto=false` gives none.
- Nightly, not per PR (the runbook §10 explains why the docker harness is too slow and noisy for PRs): add a `fresh-boot` scenario to `server/cmd/membench/scenario.go` with a 60 s warm-up (fresh volume, first-boot path). Add a `-assert-max cgroupUnreclaimableBytes=48MiB` style flag that fails the run when the median is above the bound. Run it from `.github/workflows/nightly.yml` in docker mode.

## To verify

- That `debug.FreeOSMemory()` from a timer goroutine has no bad interaction with the `GET /api/mgmt/memory?gc=1` floor mode or with the profiler. It is the same call, so it should not.
- How membench gets a usable token when the seeded admin has `MustChangePassword = true` (`server/cmd/membench/client.go:71`). Its own login is a 64 MiB argon2 verify today and becomes a bcrypt verify after step 2. Re-baseline `idle-all-sqlite` and write down that the comparison with `wiki/runbooks/memory-baselines/2026-09-04-docker-arm64.json` crosses this change.
- Whether any other startup path hashes with argon2 on a fresh install (e.g. a `SP_*` seeded admin password, demo or test run modes in `loadSampleChecks`, `job_startup.go:415-424`). Search for `passwords.Hash` callers reached from `runStartupJob`.
- Whether a bcrypt hash at cost 12 is acceptable for the seed on a Pi. It is a one-off at first boot and takes well under a second on a Pi 4. If it is not, lower the cost for the seed only. The password is public, so the cost buys nothing there.
- That the demo and SaaS deployments do not rely on the browser samples being enabled (`checkerdef.Demo`, `checkerdef.Test` sample types).
- That `/proc/meminfo` `MemTotal` is the right input for step 6 inside a container with no limit. It reports the host, which is the intended behaviour for DietPi-style native installs.
- Re-measure the target on linux/amd64 as well. Every number above is linux/arm64 under Docker Desktop.

## Acceptance Criteria

- [ ] A fresh instance (empty volume) shows RssAnon at most 48 MiB and VmRSS at most 128 MiB 60 s after start, and first-boot VmHWM at most 128 MiB, measured the same way as in this spec. Before/after numbers are recorded in the PR and in `wiki/runbooks/memory-profiling.md` §9.
- [ ] 10 s after a password login, RssAnon is back to at most 48 MiB.
- [ ] `TestArgon2MemoryIsReturned` and `TestFreshBootHeapStaysSmall` pass and run in the normal `make test`.
- [ ] The interval regexp is compiled once, and browser-dependent samples are created disabled.
- [ ] The low-memory section and the `SP_RUNTIME_*` knobs are in the public docs, and the `SP_SERVER_CHECK_WORKER_NB` default is correct there.
- [ ] The runbook's startup-burst explanation is corrected.
