# Check & Job Runners

How to configure the two runner pools (check workers and job workers) that the
SolidPing server runs internally.

## What runners actually are

Runners are **goroutines inside the `solidping` server process** — not separate
binaries, containers, or OS processes. Starting the server starts both pools
automatically. "Worker" and "runner" refer to the same goroutines; the database
`workers` row is the registration record for one server process, and each row
corresponds to a pool of in-process runner goroutines.

If you want to physically split check execution from API serving (e.g. run
checks closer to your targets), you do that by starting separate `solidping`
processes with `SP_NODE_ROLE` set — each process is then one worker with its
own runner pool.

## Node role

`SP_NODE_ROLE` selects what a given `solidping` process runs.

| Value    | API server | Check runners | Job runners |
|----------|-----------|---------------|-------------|
| `all` (default) | yes | yes | yes |
| `api`    | yes | no  | no  |
| `checks` | no  | yes | no  |
| `jobs`   | no  | no  | yes |
| `api,jobs` | yes | no | yes |
| `api,checks` | yes | yes | no |
| `checks,jobs` | no | yes | yes |

Splitting roles across processes is optional. A single `all` process is the
normal deployment.

### Combined roles (`SP_NODE_ROLE=api,jobs`)

`api`, `jobs` and `checks` may be combined in one comma-separated value. Every
single value (including the unset default, `all`) keeps its exact historic
behavior — the list form is purely additive.

Validation is strict, on purpose (a typo that quietly stops check execution
looks exactly like a healthy idle node):

- `all` and `agent` describe the whole process and cannot appear alongside
  another role — startup aborts with `node role cannot be combined with other
  roles`.
- An unknown token, a duplicate, or an empty entry (`api,`, `api,,jobs`) aborts
  startup with a message naming the offending value.
- `SP_NODE_REGION` is required as soon as `checks` is a member of the list —
  the same rule `SP_NODE_ROLE=checks` has always had. `all` still never
  requires it.
- The role can also be set through the `node.role` system parameter. That
  overlay is applied *after* startup validation, so an invalid stored value is
  refused with a WARN and the validated (env/YAML) role is kept.

The one place this matters today: `hostNetwork: true` is the practical way to
give check workers IPv6 egress on a single-stack cluster (see below), but the
node that serves the public dashboard/API should not be moved into the host
network namespace just to get it. The clean split is:

- main deployment: `SP_NODE_ROLE=api,jobs`, pod-networked, unchanged exposure;
- one checks-only deployment per region: `SP_NODE_ROLE=checks`,
  `SP_NODE_REGION=<region>`, `hostNetwork: true`, a fixed `SP_NODE_NAME`, and
  no liveness/readiness/startup probes (a checks-only node opens no HTTP
  listener).

A node that stops running checks (role changed from `all` to `api,jobs`, or a
checks pod scaled down) strands nothing: check-job leases carry
`lease_expires_at`, so any job still claimed at shutdown is reclaimed by another
worker once the lease lapses — the same path a crashed worker already takes.

## Worker identity (`SP_NODE_NAME`)

Every process that runs check or job runners registers one `workers` row. Its
`slug` and `name` come from:

1. `SP_NODE_NAME`, used **verbatim** when set — no truncation, no lowercasing,
   and the OS hostname is never read.
2. Otherwise `os.Hostname()`, lowercased for the slug and cut to the first
   **15 characters**.

The slug must satisfy the database CHECK constraint
`^[a-z][a-z0-9-]{2,20}$` (see `001_v0_1_0.up.sql`). It is validated at
**startup** — an invalid value aborts the process with a message naming the
offending slug and `SP_NODE_NAME`, instead of surfacing later as an opaque
`SQLSTATE=23514` on INSERT. The resolution lives in one place,
`config.Config.WorkerIdentity()` (`server/internal/config/worker_identity.go`),
shared by the check worker and the job worker.

Registration is an **upsert by slug**, so two processes with the same effective
slug collapse onto a single row and fight over it. Set `SP_NODE_NAME` on any
orchestrator where the hostname is not stable, not unique within its first 15
characters, or not slug-legal.

### Kubernetes `hostNetwork: true` — the reason this knob exists

A pod with `hostNetwork: true` shares the host UTS namespace, so `spec.hostname`
is silently ignored and `os.Hostname()` returns the **node** name. Node names
are usually dotted (`eu2.example.com`), which fails the slug pattern: the worker
never registers and the deployment runs no checks.

That matters because `hostNetwork` is the practical way to give check workers
IPv6 egress on a single-stack (IPv4-only) cluster — a k3s/flannel cluster
cannot be switched to dual-stack after creation, and without it an IPv6-only
target simply cannot be reached. So the recipe is: `hostNetwork: true` **plus**
an explicit `SP_NODE_NAME` (keep the value the workers already registered under,
to avoid stranding a stale `workers` row):

```yaml
      hostNetwork: true
      containers:
        - env:
            - name: SP_NODE_NAME
              value: "solidping-eu2"
```

### Truncation collisions

Kubernetes pod names are `<deployment>-<hash>-<rand>`, so
`solidping-checks-eu2-…` and `solidping-checks-us1-…` both truncate to
`solidping-check`. When the hostname is cut, the worker logs a WARN naming the
resulting slug and pointing at `SP_NODE_NAME`, so the collision is visible
rather than silent.

Note this also changes the self-monitoring check names below: they are
`int-checks-<slug>` / `int-jobs-<slug>`, where `<slug>` is the effective worker
slug — so pinning `SP_NODE_NAME` also pins those check slugs.

## Check runners

Check runners execute the active monitoring checks (HTTP, TCP, DNS, SSL,
ping, …) and the passive ones (heartbeat, email).

### Configuration

| Env var                                  | Default | What it controls |
|------------------------------------------|---------|------------------|
| `SP_SERVER_CHECK_WORKER_NB`              | `3`     | Number of concurrent runner goroutines |
| `SP_SERVER_CHECK_WORKER_FETCH_MAX_AHEAD` | `5m`    | How far in the future the fetcher will claim jobs that are about to be due |
| `SP_NODE_REGION` (or `SP_REGION`)        | `default` | Region label used to match checks tagged with region constraints |

### How many runners do I need?

`SP_SERVER_CHECK_WORKER_NB` is the **maximum number of checks that can run at
the same instant**. A check that takes 10s on a runner blocks that runner for
10s.

Picking a high value (even `1000`) is fine. Each idle runner is a parked
goroutine — the only real cost is memory (a few KB of stack each, plus
whatever the in-flight check holds). There's no thundering-herd on the
database, no extra polling, no per-runner lease churn — see the fetching
architecture below.

Pick the value based on:

- **How many checks you have × their period.** With 600 checks running every
  60s and a 1s average duration, you need ~10 concurrent runners to keep up.
- **How slow your slowest checks are.** A check has a 30s execution timeout.
  If many of your checks are slow (DNS lookups against unreachable hosts,
  HTTP probes hitting 30s timeouts), size the pool so a wave of slow checks
  doesn't starve the fast ones.

If runners are saturated, fresh jobs simply wait — they don't fail, they just
run late. The `free_runners` self-stat (reported as the
`int-checks-<slug>` internal check) tells you whether the pool has
headroom.

### Fetching architecture

There is **one fetcher goroutine per worker process, not one per runner**.
Runners never talk to the database to ask for work.

Sequence:

1. Each runner starts idle and increments an `availableRunners` counter, then
   blocks on an internal channel.
2. The fetcher reads `availableRunners`, asks the database for **at most that
   many** due jobs (`SELECT … FOR UPDATE SKIP LOCKED`), and pushes them onto
   the channel.
3. Runners that pick up a job decrement the counter and execute the check.
4. When a runner finishes, it sends a non-blocking signal on a `completion`
   channel. **That signal is what wakes the fetcher** to do another round.
5. If no runner is free, the fetcher does nothing and idles.
6. Idle runners stay idle. They are not polling, not holding leases, not
   touching the database — they are parked goroutines waiting on a channel
   receive.

The fetcher also wakes on:

- a `check.created` event (newly added check might be immediately due),
- a 60s safety timer (covers checks whose `scheduled_at` simply rolled
  forward into "now"),
- context cancellation (shutdown).

This design means the database is queried only when there's both work to do
*and* capacity to do it. Doubling `SP_SERVER_CHECK_WORKER_NB` does **not**
double the query rate — it raises the cap on how many jobs each fetch can
claim.

`SP_SERVER_CHECK_WORKER_FETCH_MAX_AHEAD` is the time window the fetcher uses
when claiming jobs. With the default `5m`, a check whose `scheduled_at` is
within the next 5 minutes is eligible; the runner then sleeps until the exact
scheduled time before executing. Raising it lets runners pre-claim further
into the future (smoother under burst load); lowering it keeps claims
tighter to wall-clock time.

## Job runners

Job runners execute background jobs: notification dispatch, email sends,
webhook delivery, aggregation, state cleanup, etc.

### Configuration

| Env var                                | Default | What it controls |
|----------------------------------------|---------|------------------|
| `SP_SERVER_JOB_WORKER_NB`              | `2`     | Number of concurrent runner goroutines |
| `SP_SERVER_JOB_WORKER_FETCH_MAX_AHEAD` | `5m`    | How far in the future the runner will pick up scheduled jobs |

### Architecture

The job worker uses a simpler **per-runner pull** model: each runner
goroutine independently calls `GetJobWait` to claim the next due job. There
is no central fetcher. Idle runners block inside that call rather than
spinning.

The same "more runners cost mostly memory" rule applies — a job that takes
30s blocks one runner for 30s, and other runners stay free to pick up
unrelated jobs.

Failed jobs marked retryable are retried up to **2 times** (hard-coded), then
marked failed.

## Hard-coded values (for reference, not configurable)

These live in the source and are not tunable via env vars today. Listed so
operators know what to expect:

- Worker heartbeat interval: **50s**
- Check job lease duration: **500ms** (renewed on the fly during execution)
- Job retry cap: **2** retries
- Fetcher error backoff (check worker): **5s**
- Fetcher periodic safety wake (check worker): **60s**
- Per-check execution timeout: **30s**

## Self-monitoring

Each running worker registers an internal check that publishes its own
stats as regular results:

- `int-checks-<slug>` — check runner pool stats (job runs, free runners,
  average duration, average delay)
- `int-jobs-<slug>` — job runner pool stats (same fields)

These show up in the `default` organization and are the easiest way to tell
whether a pool is sized correctly: if `free_runners` is consistently `0`
under steady-state load, the pool is too small; if `average_delay` is
non-zero, jobs are running late.
