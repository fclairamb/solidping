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

Splitting roles across processes is optional. A single `all` process is the
normal deployment.

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
`int-checks-<hostname>` internal check) tells you whether the pool has
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

- `int-checks-<hostname>` — check runner pool stats (job runs, free runners,
  average duration, average delay)
- `int-jobs-<hostname>` — job runner pool stats (same fields)

These show up in the `default` organization and are the easiest way to tell
whether a pool is sized correctly: if `free_runners` is consistently `0`
under steady-state load, the pool is too small; if `average_delay` is
non-zero, jobs are running late.
