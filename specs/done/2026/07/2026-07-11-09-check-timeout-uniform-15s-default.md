# Check timeout: uniform 15s default when unset, explicit 1–30s range

## Problem

Since spec 2026-07-11-05, the check form has an optional **Timeout** field
stored as the plain `timeout` key in the check `config`, the server caps it
at 30s, and the worker uses it as the execution budget with a context
deadline of `timeout + 1s`
(`server/internal/checkworker/worker.go:778-782`).

Two things still don't match the intended contract:

1. **The unset default is not 15s — it's whatever each check type picks.**
   The field's empty state reads "Check type default", and every checker
   carries its own internal `defaultTimeout`: 5s for dns/icmp/tcp/udp
   (`server/internal/checkers/checkicmp/checker.go:20`,
   `checktcp/checker.go:20`), 10s for ssh/ftp/grpc/kubernetes/…, 30s for
   browser/js (`checkbrowser/config.go:12`). So an ICMP check with no
   timeout gives up at 5s even though the worker's execution budget is the
   global 15s (`scheduling.check_timeout_ms`,
   `server/internal/checkworker/worker.go:770`). The user-facing rule
   should be simple: **no timeout set → 15s**, uniformly.

2. **The 1–30s range is only half enforced.** The client validates 1–30
   whole seconds (`web/dash0/src/components/shared/check-form.tsx:1236`),
   but the server accepts any duration `> 0 && <= 30s`
   (`server/internal/handlers/checks/timeout.go:41`) — `"500ms"` passes.
   The contract should be an explicit **1s floor** on both sides.

## Proposal

Keep the storage (`config.timeout` duration string) and the worker rule
(context cancellation = effective timeout + 1s, so a 5s ping timeout means
the server leaves at most 6s for the check to end) exactly as they are.
Change what "unset" means and pin the range.

### Uniform 15s default

- When `config.timeout` is absent, the **effective per-check timeout is
  15s** for every active check type — replacing the per-checker
  `defaultTimeout` variance as the user-visible behavior.
- Preferred mechanism: resolve the default once at the worker layer — in
  `executeJob`, unset `timeout` behaves as if `timeout = 15s` (the
  existing global `scheduling.check_timeout_ms` default), and that
  effective value is what bounds the checker, giving `context = 16s`.
  Per-checker `defaultTimeout` constants that are shorter than 15s must no
  longer cut the probe off early — either align them to a shared 15s
  constant or thread the effective timeout into the checker config when
  the key is absent (decide at implementation; the observable contract is
  "unset = 15s").
- Note the interaction with the cost-aware clamp
  (`schedParams.ExecutionTimeout`, `worker.go:770`): unset should keep
  benefiting from the EWMA-based early clamp for scheduling density, but
  the checker must be *allowed* up to 15s — only an explicit user value
  bypasses the clamp (unchanged from spec 2026-07-11-05).

### Explicit 1–30s range

- Server: `validateConfigTimeout`
  (`server/internal/handlers/checks/timeout.go`) rejects values `< 1s`
  (currently only `<= 0`), keeping the 30s cap — `VALIDATION_ERROR` on the
  `timeout` field. Whole-second granularity is not required server-side;
  the floor is.
- Client: already 1–30 (`check-form.tsx:1236`); no change beyond copy.

### UI copy

- Placeholder/hint change only — the field already exists
  (`check-form.tsx:2569`). Replace "Check type default" / "Empty uses the
  check type's default" with wording stating the 15s default, e.g.
  "15 seconds (default)" / "Seconds a single probe may run (1–30).
  Empty uses the default of 15 seconds."

### Tests

- Worker: unset `timeout` → checker observes an effective 15s bound
  (context deadline ≈ 16s) even for types whose internal default was 5s
  (e.g. icmp/tcp fake); explicit `timeout: "5s"` → deadline ≈ 6s
  (existing rule, keep asserted).
- Handler: `timeout: "500ms"` → 400 `VALIDATION_ERROR`; `"1s"` → accepted;
  `"30s"` → accepted; `"31s"` → still rejected.
- Existing checker tests asserting the old short defaults updated.

## Open questions

1. **browser/js 30s defaults**: a uniform 15s default *shortens* their
   unset behavior (30s → 15s). Accept the change for uniformity, or keep
   ≥15s defaults as-is and only raise the short ones? The spec assumes
   strict uniformity (unset = 15s everywhere) per the stated rule.
2. **Existing stored sub-second timeouts** (e.g. `"500ms"` written before
   the floor): validated on write only, so they keep working until the
   next config edit — is a migration/clamp needed, or is write-time
   enforcement enough (as spec 2026-07-11-05 chose for the 30s cap)?

## Implementation Plan

Chosen mechanism for "unset = 15s": **thread the resolved execution budget
into the checker config at the worker layer** when `timeout` is absent. This
leaves every per-checker `defaultTimeout` constant (and its direct unit tests)
untouched — those constants become dead for the production path since the
worker always supplies a `timeout` — giving the smallest blast radius while
satisfying the observable contract. Open questions resolved per the spec's
stated assumptions: (1) strict uniformity — browser/js unset now resolves to
15s like everything else; (2) write-time enforcement only, no migration for
pre-existing sub-second stored values (mirrors spec 2026-07-11-05's 30s cap).

1. **Worker — uniform default** (`server/internal/checkworker/worker.go`):
   Resolve `checkTimeout` (cost-aware clamp, or explicit per-check user value
   which bypasses the clamp) *before* parsing the checker config. When the
   user left `timeout` unset, thread the resolved budget into a copy-on-write
   config map (`configWithDefaultTimeout`) so the checker honors it instead of
   its own short internal `defaultTimeout` (e.g. icmp's 5s). The original job
   config map is never mutated, so the clamp decision still reflects the
   user's real input, and the execution context stays `checkTimeout + 1s`.
2. **Server — 1s floor** (`server/internal/handlers/checks/timeout.go`):
   `validateConfigTimeout` rejects `< 1s` (was `<= 0`), keeping the 30s cap;
   error message updated to `must be >= 1s and <= 30s`.
3. **Frontend copy** (`web/dash0/src/components/shared/check-form.tsx`):
   placeholder `Check type default` → `15 seconds (default)`; hint → `Seconds a
   single probe may run (1–30). Empty uses the default of 15 seconds.` Client
   1–30 validation is already in place.
4. **Tests**:
   - Worker (`worker_test.go`): new test — unset `timeout` → checker config
     observes an effective `15s` bound and context deadline ≈ 16s. Existing
     explicit-timeout test (deadline = timeout + 1s) kept.
   - Handler (`timeout_test.go`): `500ms` → rejected; `1s`/`30s` → accepted;
     `31s` → still rejected; updated messages.
