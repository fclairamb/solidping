# Management

Server-level introspection and support endpoints. These live under
`/api/mgmt` (not `/api/v1`), except for the feature-flag endpoint.

### GET /api/mgmt/health
Health check. Auth: public

### GET /api/mgmt/version
Returns server version, build hash, and build date. Auth: public

### GET /api/mgmt/limits
Introspection of the server's configured limits — the rate-limiting section
(window, burst, per-route overrides) and the concurrency section. Useful for
clients that want to pace themselves and for support when diagnosing 429s.
Auth: public

### GET /api/mgmt/memory
A precise memory breakdown of the server process, wrapped as `{ "data": {...} }`.
Auth: super-admin

Sections:

| Section | Contents |
|---|---|
| `runtime` | MemStats (heap alloc/inuse/objects, stack, sys, GC counters), `goMemLimitBytes`, `goMaxProcs`, plus `classes` — the `runtime/metrics` `/memory/classes/*` breakdown and `/gc/heap/live` |
| `process` | `rssBytes` (unchanged: `process_resident_memory_bytes` = anon + file + shmem), `status` (`/proc/self/status`: `rssAnonBytes`, `rssFileBytes`, `rssShmemBytes`, `vmHwmBytes`, `threads`), `smaps` (`/proc/self/smaps_rollup`: `pssBytes`, `privateDirtyBytes`, `privateCleanBytes`, `sharedCleanBytes`) |
| `cgroup` | The container's own accounting: `version`, `currentBytes`, `peakBytes`, `maxBytes`, and the `memory.stat` keys `anon`, `file`, `fileMapped`, `kernel`, `slab`, `sock`, `shmem`, plus the derived `unreclaimableBytes` |
| `derived` | `offHeapBytes` = `rssAnonBytes − (classes.totalBytes − classes.heapReleasedBytes)`, `offHeapKnown`, `goResidentBytes` |
| `subsystems` | DEK cache / rate-limit / event-listener cardinalities |
| `build` | `cgoEnabled`, `sqliteDriver`, `goVersion` |
| `sample` | `mode` (`steady` \| `floor`), `gcForced`, `takenAt` |

Three things worth knowing before quoting any of these numbers:

- **They are not the same "RSS".** `kubectl top` shows the cgroup *working set*
  (includes actively mapped pages of the binary); `rssBytes` is anon + file +
  shmem with no split; the Go runtime sees neither the cgo arena nor the
  binary's rodata. Only `cgroup.unreclaimableBytes` (`anon + kernel`) is what an
  OOM kill is decided on — it is the primary metric of `make bench-memory`.
- **Linux-only sections degrade, they do not fail.** On macOS, cgroup v1, or no
  cgroup at all, `process.status`, `process.smaps` and `cgroup` report
  `present: false` with zero values; `derived.offHeapKnown` goes false rather
  than passing a fabricated zero off as a measurement. The request still
  succeeds.
- **`?gc=1` is a different measurement.** It runs `runtime.GC()` +
  `debug.FreeOSMemory()` before sampling and reports `sample.mode: "floor"`.
  The floor and the steady state are both legitimate; comparing one against the
  other is how a "10 % reduction" turns out to be a GC phase.

The same numbers are on `/metrics` as `solidping_process_rss_anon_bytes`,
`solidping_process_rss_file_bytes`, `solidping_process_smaps_pss_bytes`,
`solidping_cgroup_memory_{current,peak,max,anon,file,kernel,unreclaimable}_bytes`
and `solidping_process_offheap_bytes`. Where a source is absent, **no series is
emitted** rather than a zero.

See `wiki/runbooks/memory-profiling.md` for how to use them.

### GET /api/mgmt/scheduling/cost-distribution
Distribution of scheduling cost across checks/lanes — used to diagnose an
unbalanced scheduler. Auth: super-admin

### GET /api/mgmt/email-preview
List every previewable email template, wrapped as `{ "data": [...] }`. Each row
carries `template` (the file name, which is the `:template` path segment
below), `subject` (rendered through the real formatter), `hasText` (whether the
template ships a plaintext part), `previewUrl`, and `error` when that template
failed to render with its fixture. Backs the dashboard's email catalog at
`/dash0/orgs/:org/test/emails`. Registered only when `SP_RUNMODE=test`.
Auth: public (test mode only)

### GET /api/mgmt/email-preview/:template
Render an email template with sample data so it can be reviewed in a browser
without sending anything. `?format=html` (default) or `?format=text`.
`?colorScheme=light` (default) or `?colorScheme=dark`; `dark` applies only to
the HTML format and rewrites the template's own
`@media (prefers-color-scheme: dark)` block to `@media all`, so an `<iframe>`
— which cannot be told to report a dark preference — shows the exact CSS a
dark-mode client applies. Without the param the response is the untouched
template. Any other value is a 400. See
[features/email-dark-mode.md](../features/email-dark-mode.md).
Registered only when `SP_RUNMODE=test`. Auth: public (test mode only)

### POST /api/mgmt/report
Submit an in-app bug report (multipart/form-data). Public endpoint, optional bearer token for user attribution. Body fields: `url` (required), `comment`, `org`, `annotations`, `context` (JSON), `screenshot` (file). Returns `{ uid }`. The screenshot is stored as a `File` (group `reports`) and a GitHub issue is created asynchronously when `app.github.*` is configured.

### GET /api/v1/features
Return the active feature flags for the frontend (e.g. `{ "bugReport": true }`). Auth required.
