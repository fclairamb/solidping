# Platform redundancy via DNS failover (no Cloudflare, no PaaS)

Design discussion date: 2026-08-07 (Claude + Florent).

## Problem

SolidPing production runs on a single hosting server: if that machine (or its
datacenter) dies, the whole platform is down — API, dashboards, customer status
pages, and job dispatch to workers — and there is no rehearsed path to bring it
back elsewhere. We want cheap platform redundancy: the ability to redirect all
traffic to a second hosting server, with full PostgreSQL redundancy, **without**
a big PaaS and **without** putting Cloudflare in front (plain DNS switching is
the chosen traffic-failover mechanism).

Constraints and risks the design must own:

- **DNS failover tail**: some resolvers ignore TTLs, so a flip is ragged —
  a minority of traffic keeps hitting the old IP for minutes to hours.
- **Who decides**: a watchdog running *on the failing cluster* cannot reliably
  detect or act on its own death; the decider must be external.
- **Split-brain**: with only two nodes, automatic Postgres promotion is how you
  end up with two primaries and diverging writes (duplicate/contradictory
  alerts, painful merges).
- **Replication is not backup**: a bad migration or fat-fingered DELETE is
  faithfully replicated to the standby in milliseconds.

## Proposal

Two servers in different datacenters, async Postgres streaming replication,
and a DNS flip driven by an external watchdog — with the key design decision
that **the DNS flip and the DB promotion are separate actions with separate
risk profiles and separate triggers**.

### 1. Backups first (prerequisite, biggest risk reduction per euro)

- Continuous WAL archiving with **pgBackRest or WAL-G** to object storage
  (S3/B2/R2), giving point-in-time recovery to any second.
- This protects against the failure classes replication makes *worse*
  (bad migration, corruption, accidental deletes) and doubles as the bootstrap
  source for the standby.
- Monitor archive freshness.

### 2. Postgres: hot standby, manual promotion

- Built-in **async streaming replication**: `pg_basebackup` onto server 2,
  `primary_conninfo` + `standby.signal`. Typically sub-second lag.
- **No auto-promote** with two nodes. Detection is automatic, promotion is one
  manual script (`failover.sh`) run by a human:
  1. Fence the old primary if reachable (stop app + Postgres over SSH /
     out-of-band); skip if unreachable.
  2. `pg_ctl promote` on the standby.
  3. Verify the app on server 2 accepts writes.
- **Fencing rule**: the old primary never comes back as primary. On return it
  rejoins as a standby via `pg_rewind` or a fresh `pg_basebackup` (scripted,
  documented — never improvised at 3am). Flip-back is manual, only after it
  has caught up as a standby.
- Expected posture: RTO ~5–15 min including human reaction, RPO ~replication
  lag (seconds).

### 3. DNS: one flip-point, 60s TTL, API-driven

- **CNAME everything to a single flip-point** (e.g. `edge.solidping.io`):
  app host, agent WS host, `docs.solidping.io`, and the hostname customer
  status pages CNAME to. Failover = change one A/AAAA record.
- Apex `solidping.io` can't CNAME: use a provider with ALIAS/ANAME support, or
  have the script flip the apex A record alongside.
- **TTL 60s** — lower buys nothing (resolvers clamp below ~30s) and costs
  query load. Accept the straggler tail: those clients see an outage, not
  wrong data.
- Pick the DNS provider for its **update API** and anycast footprint (deSEC,
  Gandi LiveDNS, Bunny DNS…). The provider becomes a real SPOF — choose
  accordingly. (Route53 health-checked failover records at ~$1/mo would do the
  flipping provider-side, if ever acceptable.)

### 4. Watchdog: automate the flip, gate the promotion

- **DNS flip is low-risk and reversible → automate aggressively.** Flipping to
  a secondary whose Postgres is still a read-only standby yields a degraded
  but honest service: status pages render, dashboards load read-only, writes
  fail. Even a false positive doesn't diverge data.
- Watchdog runs **on the secondary**, probes the primary's health endpoint
  every ~10s, and requires **corroboration from a second network path** before
  acting (probe via a Fly machine, or an external uptime check agreeing).
  After ~3 consecutive confirmed failures: flip the record and page the
  operator.
- **Postgres promotion stays manual** (see §2). Net effect: automatic
  time-to-first-byte recovery (read-only within ~2–3 min, no human), while
  the irreversible step waits for a human.

### 5. TLS certificates (the classic gotcha)

- HTTP-01 can't issue on the secondary until DNS already points at it —
  chicken-and-egg mid-outage. Use **DNS-01 challenges on both servers**
  (scoped DNS API token — the secondary holds one for the watchdog anyway),
  or a wildcard issued via DNS-01 and synced.
- Verify *before the first rehearsal* that the secondary serves valid TLS for
  every flipped hostname, including customer status-page custom domains.

### 6. App layer & workers

- The Go binary is stateless (frontend embedded): deploy and run it on both
  servers; the secondary points at the local standby and serves reads / fails
  writes until promotion.
- **Deported agents follow for free**: they reconnect over WebSocket by
  hostname and Go re-resolves DNS per dial, so they follow the flip within the
  TTL. Verify nothing in the agent reconnect path caches a resolved IP across
  attempts.
- The in-cluster `SP_NODE_ROLE=checks` worker on the secondary starts claiming
  jobs the moment its local Postgres becomes primary.
- Note: status0 is served by the same binary, so the status pages are down
  until the flip completes — an argument for fast flips, and eventually for an
  independent status-page edge.

### 7. Dogfood monitoring

- SolidPing check on **replication lag** (a stale standby silently converts
  ~zero RPO into the lag; alert past a few seconds).
- SolidPing check on **the watchdog itself** (a dead watchdog is a silent loss
  of the entire failover capability).

### Rollout order

1. WAL archiving + PITR to object storage (hours of work).
2. Second server: streaming replica + app deployed and running (~1 day).
3. Watchdog + DNS API flip + `failover.sh` + runbook (~1 day).
4. **Rehearse once for real**: flip, promote, serve, flip back, re-clone.
5. Only if manual promotion ever proves too slow: third witness node and
   automated promotion.

Ongoing cost: one extra VPS + a few euros of object storage.

## Acceptance Criteria

- [ ] Continuous WAL archiving to object storage with verified PITR restore
- [ ] Hot standby on a second server in a different DC, lag monitored
- [ ] All public hostnames CNAME to a single flip-point; TTL 60s
- [ ] Watchdog on the secondary with second-path corroboration; automatic DNS
      flip + operator page
- [ ] `failover.sh`: fence → promote → verify; rejoin path scripted
      (`pg_rewind` / re-clone)
- [ ] Both servers hold valid TLS for all flipped hostnames via DNS-01
- [ ] Deported agents verified to re-resolve DNS on reconnect
- [ ] Full failover rehearsed end-to-end at least once (flip, promote, serve,
      flip back)
