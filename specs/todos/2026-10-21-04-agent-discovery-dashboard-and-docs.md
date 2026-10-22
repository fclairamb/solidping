---
model: sonnet
effort: medium
---

# Dashboard and docs do not know discovery can run from a private location

Last of four specs for agent-run network discovery. Needs spec 03 (API `region`,
`serverAvailable`, `capabilities.discovery`, progress fields, audit events).

## Problem
Once spec 03 ships, the API can run a LAN scan from a private region, but:
- the scan form has no way to pick one (`web/dash0/src/routes/orgs/$org/organization.discovery.new.tsx`);
- in SaaS, `lan` only appears with `serverAvailable: false`, so it needs a region to be usable;
- scan details, discovered rows and promotion do not say where a scan ran or where promoted
  checks will run;
- the docs say the opposite of the new behaviour. `web/docs/docs/features/discovery.md:64-67`
  says discovery never runs from an agent, and `private-locations.md:19-22` says agents are
  "checks-only by construction".

## Proposal
1. **Scan form** (`organization.discovery.new.tsx`):
   - Add a single-select `<Select>` shaped like the scan-method select (`:269-281`), shown for
     `lan` only.
   - Options: "Server (default)", plus the private regions from `useRegions(org)` filtered on
     `r.private`. Send `RegionDefinition.slug` (`@dc1`), never `PrivateRegion.slug` (raw `dc1`).
   - A region whose `capabilities.discovery` is not `yes` is disabled, with a hint to set
     `SP_AGENT_DISCOVERY_ENABLED` on its agent. The capability is a hint, as for ipv6 in
     `check-form.tsx`.
   - When the type reports `serverAvailable: false`, hide "Server" and require a region.
   - Do not reuse the check-form checkbox grid (`check-form.tsx:1856-1965`): it breaks the strict
     single-checkbox selectors in `e2e/discovery.spec.ts:91, 124`.
   - `data-testid`s: `discovery-region-select`, `discovery-region-option-<slug>`.
   - Add the pattern to `design-reference.tsx`.
2. **Errors.** Map `DISCOVERY_REGION_UNAVAILABLE` (and `DISCOVERY_QUOTA_EXCEEDED` if spec 03
   open question 2 is accepted) in the form's error handling (`:238-248`).
3. **Scan detail** (`organization.discovery.$jobUid.index.tsx`):
   - a "Runs from @dc1" line;
   - in the progress card (`:454, :462`), `failedChunks` with the translated `lastErrorCode`, so a
     scan whose agent refused every chunk does not look like an empty success;
   - a region badge on discovered rows;
   - a line in the promote confirmation saying promoted checks will run from `@dc1`.
4. **Private locations list** (`private-locations.index.tsx:297-303`): a "discovery" capability
   badge, like the existing ones.
5. **Events**: labels for `discovery.scan_started`, `discovery.scan_canceled` and
   `discovery.chunk_refused` in both maps of `components/dashboard/event-display.tsx`
   (`:130-131`, `:386-387`) and in `locales/*/events.json`.
6. **Copy.** Update `workerNote` in `locales/{en,fr,de,es}/discovery.json:66`. Every new string
   goes into all 4 locales (`locale-parity.test.ts`).
7. **User docs** (`web/docs/docs/`):
   - `features/discovery.md` (`:34-47, 64-67, 74-76`): scans can run from a private location, the
     egress table, and cancel semantics for agent chunks (the agent stops at its next heartbeat,
     30 s at most).
   - `configuration/egress-policy.md` (`:217-226`): under an enforcing policy, LAN runs through a
     private region only.
   - `features/private-locations.md`:
     - fix the "checks-only by construction" claim (`:19-22`);
     - add `SP_AGENT_DISCOVERY_ENABLED` and `SP_AGENT_DISCOVERY_CIDRS` to the env table
       (`:133-145`);
     - add a new "Scan the location's network" section between `:300` and `:359`;
     - fix the Security model row "claim/result only" (`:363`);
     - add Known limitations: the allow-list bounds discovery only, while checks and trace
       requests can already reach any host the agent routes to (network filtering of the agent
       host is the real boundary); reverse DNS of live hosts goes to the agent's resolver; ICMP
       needs `NET_RAW` or `net.ipv4.ping_group_range`, otherwise liveness is TCP-only; the
       per-replica handshake replay window (`:375-389`) also applies to discovery claims.
   - `features/events.md`: the three new event types.
8. **Wiki**:
   - `wiki/features/deported-agents.md` (`:175-180, 212-232`): the HTTP discovery endpoints,
     agent pull, and system agents must never set `SP_AGENT_DISCOVERY_ENABLED` (`:415-422`).
   - `wiki/conventions/regions.md` audit table (`:76-88`): a row for the discovery claim predicate
     (org + exact region, system agents excluded).
   - `wiki/api-specification/discovery.md`: document the `{type, parameters, region?}` body and
     `serverAvailable`. Fix the existing errors: the progress block returns `groupCount` /
     `checkCount`, not `hostCount` (`:23, :29`), and cancel interrupts running chunks (`:32`).
   - `wiki/api-specification/agents.md`: the `discovery` capability and the agent discovery
     endpoints.
   - `wiki/database-model/discovery.md`: the `discovery_chunks` table.

## Tests
- `web/dash0/e2e/discovery-region.spec.ts`, mocking `/regions` and `/discovery/types` with
  `page.route` like `discovery-egress-policy.spec.ts:23-29`:
  - the picker is hidden for non-lan methods;
  - only private regions are listed, never cloud ones;
  - a region without `discovery: yes` is disabled;
  - with `serverAvailable: false`, "Server" is absent and submit without a region is blocked;
  - the request body carries `region: "@dc1"`;
  - `DISCOVERY_REGION_UNAVAILABLE` shows its translated message.
- `e2e/discovery.spec.ts` still passes unchanged (single-checkbox and single-textarea selectors,
  `:91, :116, :123, :124`).
- `locale-parity.test.ts` passes. `make lint`. The docs site builds.
