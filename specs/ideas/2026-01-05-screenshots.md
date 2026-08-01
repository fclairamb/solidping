# Screenshot capture on check failure

Capture what a failing page looked like, and show it on the incident — the
BetterStack "see what your users saw when it broke" bullet.

**Rewritten 2026-08-01.** The original (2026-01-05) predates the browser
checker, the file-storage layer, and the deported-agent protocol. Three of its
four core decisions are now obsolete and a fourth was wrong on its own terms;
see [§7 What changed](#7-what-changed-from-the-2026-01-05-version) for the
diff and the reasoning. This version is an **idea with a verdict**, not a
ready-to-implement plan — the open question in §6 must be answered before
Phase 2 is written.

**Verdict up front**: worth doing, at roughly a fifth of the originally scoped
cost, and *not* as a new microservice. Nearly all the machinery already exists
in-tree. The real prerequisite is unglamorous and has standalone value: there
is no Chrome in any container image, so the shipped `browser` check type is
broken in every deployment today. Fix that first; screenshots then ride on it
for about a day of work.

---

## 1. What already exists

Every one of these landed after the original spec was written.

| Piece | Where | State |
|---|---|---|
| Headless Chrome checker | [`checkers/checkbrowser/checker.go`](../../server/internal/checkers/checkbrowser/checker.go) | Shipped, on **chromedp** v0.16.0 |
| Blob storage seam | [`handlers/filestorage/filestorage.go`](../../server/internal/handlers/filestorage/filestorage.go) | Shipped — `FileStorage` iface, scheme registry |
| Local FS + S3 backends | `filestorage/localfs`, `filestorage/s3fs` | Shipped, incl. MinIO/Garage/Ceph path-style |
| `screenshots` storage group | [`filestorage.go:28`](../../server/internal/handlers/filestorage/filestorage.go) | **Already declared**: `GroupTypeScreenshots` |
| `files` table + service | [`handlers/files/service.go`](../../server/internal/handlers/files/service.go) | Shipped — org-scoped, mime, sha256, soft-delete |
| Signed download URLs | [`handlers/files/signedurl`](../../server/internal/handlers/files/signedurl/signedurl.go) | Shipped — HMAC + TTL |
| Storage config | `config.FileStorageConfig` | Shipped — `SP_FILESTORAGE_*` |
| Agent request signing | [`agents/crypto.go:179`](../../server/internal/agents/crypto.go) | Shipped — Ed25519 over (method, path, ts, nonce) |

**Consequence**: the original spec's Phase 3 ("serve screenshot from S3") is
already written, and its storage section describes building a second,
org-unaware storage path parallel to the one in the tree. Drop both.

### The one thing that does not exist

`grep -i chrome Dockerfile docker-compose*.yml Makefile` → **zero matches.**

The `browser` check type only works where Chrome happens to be on the host —
a dev laptop. In every container deployment it takes the
"Chrome/Chromium not found" branch of `handleBrowserError`. On top of that,
`runBrowser` calls `chromedp.NewExecAllocator` **per check execution**: a
fresh browser process, every run, every check.

This is the actual blocker, and it is a bug independent of screenshots.

---

## 2. Technology: stay on chromedp

The original chose Rod. Reverse that.

The repo already ships a chromedp-based checker. Adding Rod means two CDP
stacks, two browser-lifecycle models, and two sets of zombie-process failure
modes — to capture one PNG when a check fails.

The Rod case in [`wiki/research/screenshot-tools.md`](../../wiki/research/screenshot-tools.md)
(multiplexed events, decode-on-demand memory, `launcher.Manager`) is a
**high-throughput screenshot farm** argument. That is not this workload:
captures happen on incident transitions, which are rare by construction. The
research remains valid; it was answering a question we turned out not to be
asking.

If chromedp's fixed-size event buffer ever becomes a measured problem under
real load, revisit then — with a benchmark, not a table.

---

## 3. Architecture

### 3.1 Browser lifecycle (Phase 0)

Replace per-execution `NewExecAllocator` with a long-lived headless-shell and
`chromedp.NewRemoteAllocator` against it. Two deployment shapes, same code
path:

- **Self-hosted / compose**: `chromedp/headless-shell` as a sidecar container,
  `shm_size: 2g`. Worker points at its CDP endpoint.
- **Single-binary self-host**: no Chrome → `browser` checks report
  `StatusError` with today's clear message. Unchanged, but now *deliberate*
  rather than accidental.

Config: `SP_BROWSER_CDP_URL` (empty ⇒ browser features disabled, and the
capability is not advertised).

While here, wire up the capability that already exists on paper.
`labelReqChrome` is declared at [`checkerdef/types.go:222`](../../server/internal/checkers/checkerdef/types.go)
and **nothing reads it** — the scheduler will happily dispatch a browser job
to a Chrome-less agent, which then fails every run forever. Agents should
advertise capabilities on enroll and the allocator should respect them.

### 3.2 Capture points

Not "on every failed execution". On **incident transitions**:

```
incident opens   → capture "before" shot   (what broke)
incident resolves→ capture "after" shot    (optional, off by default)
```

A flapping 30 s check produces ~2,880 failed executions/day. It produces a
handful of incidents. This is the difference between a feature and a storage
incident of our own.

### 3.3 Storage — reuse `files`, key on the incident

```go
file, err := filesSvc.CreateFile(ctx, orgUID,
    filestorage.GroupTypeScreenshots,
    "incident-<uid>-open.png", "image/png",
    nil /* createdBy: system */, bytes.NewReader(png), int64(len(png)))
```

That is the whole storage story. `CreateFile`
([service.go:201](../../server/internal/handlers/files/service.go)) writes the
blob through the configured backend and the metadata row in one call; signed
URLs and the `GET` handler already exist.

**Link from the incident, not the result.** The original keyed screenshots on
`resultUid`. Raw result rows are rolled up and **deleted** after
`defaultRetentionRawHours = 24`
([job_aggregation.go:312](../../server/internal/jobs/jobtypes/job_aggregation.go)),
so a result-keyed screenshot is an orphan blob with no reachable parent within
a day — while the thing a user opens three days later is the *incident*, which
persists.

Store the link in the existing `incidents.details` JSONB bag
([models/incident.go:50](../../server/internal/db/models/incident.go)):

```json
{ "screenshots": { "open": "<fileUid>", "resolved": "<fileUid>" } }
```

No migration. If screenshots later need their own query surface (list all
shots for a check, GC by age), promote to an `incident_files` link table then.

### 3.4 Check configuration

```json
{ "url": "https://example.com", "screenshot": true }
```

Opt-in, default `false`. **On the `browser` check type first** (§5, Phase 1) —
it already has a live page, so capture is `chromedp.FullScreenshot` into a
buffer at near-zero marginal cost and introduces no new dependency for anyone.
HTTP checks come in Phase 2 and depend on §6.

---

## 4. What the screenshot actually proves

Worth stating plainly, because the marketing bullet overclaims and the UI
should not.

The capture happens **after** the failure was detected, from a different
browser context, possibly seconds later — and for a deported agent, from a
different network position than the probe. It shows "what a browser saw
shortly after the check failed", not the failure frame.

That is still useful (blank page vs. 502 page vs. cert interstitial vs. "looks
fine, so it's the network"). It is not a replay. **Surface the capture
timestamp and the capturing region next to the image**, and never label it as
the moment of failure.

---

## 5. Phases

Each phase ships independently and has value on its own.

**Phase 0 — Chrome, for real** *(the prerequisite; fixes an existing bug)*
- Chrome/headless-shell available to the worker; `SP_BROWSER_CDP_URL`
- `RemoteAllocator` + shared browser, replacing per-execution `ExecAllocator`
- Agent capability advertisement; allocator respects `requires:chrome`
- Compose sidecar + docs
- *Ships:* the `browser` check type works in containers for the first time

**Phase 1 — screenshots on browser checks** *(~1 day on top of Phase 0)*
- `screenshot: true` in `BrowserConfig`
- Capture into a buffer on incident open; `filesSvc.CreateFile` into
  `GroupTypeScreenshots`; file UID into `incidents.details`
- Incident detail page renders it via the existing signed-URL endpoint
- *Ships:* the demo and the competitive parity bullet

**Phase 2 — screenshots on HTTP checks** *(blocked on §6)*
- Requires answering the agent-upload question
- Requires an entitlement gate + per-org storage quota **from the start** —
  this is the first feature that lets a user write unbounded bytes. Slot
  alongside `maxChecks` / `maxUsers` in `org_entitlements`, surfaced on the
  org **Usage** page.

**Phase 3 — retention & GC** *(not optional, not "later")*
- Reap screenshot blobs with their incident, on the incident retention clock
- Orphan sweep for blobs whose `files` row lost its incident
- Blobs with no reaper is how a storage bill becomes a surprise

---

## 6. Open question — how does a deported agent upload bytes?

**This must be answered before Phase 2 is written.** The original spec does not
mention agents at all.

Checks run on deported agents over the WS protocol in
[`agents/protocol.go`](../../server/internal/agents/protocol.go). The `result`
frame carries `Metrics` and `Output` maps — JSON, no binary channel. An agent
has no DB handle and no object-storage credentials, **by design**: the whole
sealed-credential architecture exists to keep agents holding as little as
possible. So "worker uploads to S3" is only true for the in-cluster worker.

Three options:

| Option | Verdict |
|---|---|
| Base64 in the `Output` map | **No.** 100–500 KB PNGs through the JSON control channel; bloats result rows and the notifier path. |
| Ship S3 creds to agents | **No.** Directly contradicts the sealed-credentials posture for a cosmetic feature. |
| **Signed HTTP upload endpoint** | **Preferred.** |

The third option needs no new crypto. `agents.VerifySignature(publicKey,
method, path, timestamp, nonce, sig)`
([crypto.go:90](../../server/internal/agents/crypto.go)) is generic over method
and path; it is currently only wired into the WS upgrade
([agentws/handler.go:374](../../server/internal/handlers/agentws/handler.go)).
Reuse it on a `POST /api/agent/v1/screenshots` that accepts `image/png`,
authenticates by agent identity, and returns a file UID the agent then
references in its `result` frame. Server-side: size cap, mime sniff, rate
limit per agent, and the org derived from the job — never from the request
body.

Sub-questions still open:
- Does the agent upload before or after submitting its result? (After, with
  the file UID in a follow-up frame, avoids blocking result reporting — but
  adds a frame type.)
- What happens when upload succeeds and the result frame is lost? (Orphan
  blob → Phase 3's sweep must cover it.)

---

## 7. What changed from the 2026-01-05 version

| Original decision | Now | Why |
|---|---|---|
| Use Rod | **chromedp** | Repo already ships a chromedp checker; Rod's edge is a farm-scale argument that does not apply |
| Separate containerized screenshot service, one per region | **In-worker capture against a shared headless-shell** | The isolation argument was already lost — the worker spawns Chrome today. A per-region service doubles a deploy surface that is already manually tag-synced across 3 Deployments |
| `SP_S3_*` env vars, direct S3 write | **Existing `filestorage` + `files` service** | Already built, org-scoped, with signed URLs and a `screenshots` group already declared |
| `ALTER TABLE check_results ADD screenshot_path` | **`incidents.details` JSONB** | Raw results are deleted after 24 h — a result-keyed screenshot orphans itself. Also, active work is making that table *smaller* |
| Capture on every failed execution | **Capture on incident transitions** | ~2,880 vs. a handful of blobs/day for a flapping 30 s check |
| HTTP checks first | **Browser checks first** | Browser checks already hold an open page; HTTP-check capture depends on the unanswered §6 |
| Retention as "Phase 4" | **Phase 3, non-optional** | Unreaped blobs are a billing surprise |
| *(absent)* | **Phase 0: Chrome in the image** | The shipped `browser` check type is broken in every container deployment today |
| *(absent)* | **§6 agent upload path** | The original assumes every worker can reach S3; deported agents cannot |

---

## 8. Honest note on priority

This is a **sales bullet, not a diagnostic feature**. The screenshot confirms
the page was broken — which you already knew, because there is an incident. It
rarely tells you *why*. Response-body capture and traceroute are more useful
per unit of effort.

[`wiki/roadmap.md:45`](../../wiki/roadmap.md) rates it P1 for BetterStack
parity, which is a legitimate reason to build it. The point of this rewrite is
that parity costs about two days on top of a Chrome fix we owe the `browser`
check type anyway — not a new microservice, a second CDP library, and a
parallel storage path.
