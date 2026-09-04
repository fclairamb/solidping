# BetterStack vs SolidPing — Comparison & Lessons

Side-by-side capability check, gap analysis, and a distilled list of design patterns to adopt or avoid. The full design-pattern recommendations live in [wiki/research/alerting-patterns.md](../../research/alerting-patterns.md); this page is the BetterStack-scoped version.

## Pricing model

BetterStack uses **modular subscription pricing** — three sub-products billed separately, sometimes bundled:

- Uptime
- Incident management / on-call (formerly Better Stack On-call)
- Telemetry (formerly Logtail)

| Tier | Price | Notable |
|---|---|---|
| Free | $0 | 10 monitors · 10 heartbeats · 3-min checks · 1 status page |
| Combined plan example | ~$269/mo | 60 monitors · 6 team members · 2,000 status-page subscribers · unlimited alerts ("all-you-can-alert") · 30-s checks |

Marketing claim: replaces ~$673/mo of PagerDuty + Pingdom + Statuspage.io. The "unlimited alerts" framing is the headline — no per-incident or per-SMS charge.

## Capability matrix

| Capability | BetterStack | SolidPing (May 2026) |
|---|---|---|
| HTTP / HTTPS monitoring | ✅ | ✅ |
| Keyword (presence / absence) | ✅ literal substring | ✅ string + regex |
| JSON-path body validation | ❌ | ✅ |
| Ping (ICMP) | ✅ | ✅ |
| TCP / UDP port | ✅ | ✅ |
| DNS | ✅ | ✅ |
| SMTP / POP3 / IMAP | ✅ | ✅ |
| SSH / FTP / SFTP | ❌ | ✅ |
| WebSocket / gRPC | ❌ | ✅ |
| Database / queue / SNMP | ❌ | ✅ |
| Heartbeat / cron | ✅ + `/fail` + exit-code path | ✅ |
| Heartbeat `/start` endpoint | ❌ | ❌ (gap, both) |
| Browser checks | ✅ Playwright (Chromium), real test scripts | ✅ chromedp/CDP (Chromium), page load + selector + keyword — not a script runner |
| SSL / domain expiration | ✅ | ✅ |
| Multi-region quorum | ✅ hardcoded 3-of-N | Per-region results, no quorum |
| `confirmation_period` (time) | ✅ seconds | ❌ uses count-based `IncidentThreshold` |
| `recovery_period` (time) | ✅ flap-resets | ❌ uses adaptive count-based threshold |
| `validating` transient state | ✅ | ❌ |
| Escalation policies | ✅ 4 step types | ✅ user / schedule / connection / all_admins |
| `instructions` step (runbook on incident) | ✅ markdown + checkboxes + reminders | ❌ |
| `time_branching` step | ✅ | ❌ |
| `metadata_branching` step | ✅ typed catalog | ❌ |
| On-call rotations | ✅ hour / day / week | ✅ daily / weekly |
| Concurrent shifts (N people on at once) | ❌ | ❌ (both gap; Hyperping has it) |
| Acknowledgement | ✅ forever-until-resolve | ✅ + manual resolve |
| Time-bounded snooze | ❌ (only AI silencing) | ✅ |
| Status pages (custom domain) | ✅ | ✅ |
| Status-page subscribers (email / RSS / webhook) | ✅ + JSON API endpoint | ❌ (Tier-2 gap) |
| Maintenance windows | ✅ weekday-time recurrence | ✅ + explicit start/end + recurrence |
| Group-incident correlation | ✅ time-window | ✅ check-group rollup |
| Telegram / MS Teams / PagerDuty channels | ✅ native | ❌ (specs ready) |
| Outgoing webhook custom payloads | ✅ body + headers + method | ✅ |
| Outgoing webhook HMAC signing | ❌ | ❌ (both gap; differentiator opportunity) |
| Terraform provider | ✅ first-party (31 resources) | ❌ (Tier-2 gap) |
| Encryption at rest (envelope) | undocumented | ✅ |
| Self-hosted | ❌ | ✅ |
| AI: smart merging / silencing / post-mortems | ✅ marketing | ❌ |

## SolidPing's unique advantages

- **Self-hosted, single binary** — full data ownership.
- **Broader protocol matrix** — SSH, gRPC, WebSocket, FTP/SFTP, databases (Postgres/MySQL/MSSQL/Oracle/Mongo/Redis), message queues (Kafka/RabbitMQ/MQTT), SNMP, game servers, sandboxed JS check, email-inbox passive monitoring.
- **JSON-path / regex body validation** built into HTTP checks.
- **Time-bounded snooze** as a first-class operation (BetterStack only has ack-forever or AI silencing).
- **Maintenance with explicit start/end timestamps** alongside weekday-time recurrence.
- **Encryption at rest with envelope encryption + per-org DEKs**.
- **MCP server** for AI/LLM tool integration — no SaaS competitor has this.
- **No vendor lock-in, no SaaS dependency** for regulated/air-gapped users.

## SolidPing gaps that BetterStack closes (worth borrowing)

The high-leverage list, ranked by SolidPing impact:

1. **`recoveryPeriod` (seconds, flap-resets)** — clean, simpler than the adaptive count-based threshold we use today. See [monitoring.md](monitoring.md#recovery--recovery_period).
2. **`confirmationPeriod` (seconds)** alongside (or replacing) `IncidentThreshold` — wall-clock decouples from `checkFrequency`.
3. **`validating` transient status** — visible-to-user "we're confirming this failure".
4. **`instructions` escalation step type** — markdown comment + `- [ ]` checklists posted into the timeline, with reminder interval. Cheapest "runbook on the incident" pattern.
5. **Severity primitive** that decouples "channel matrix" from "step routing". Pre-empts the dual-surface confusion BetterStack has between `monitor.email/sms/call/push` and `policy.steps[].urgency_id`.
6. **Override events as `override: true`** on a single events table — no separate "override" object class.
7. **Status-page subscriber notifications** — email + webhook + RSS + JSON endpoint; verify-by-link; component-level filtering. Already a Tier-2 roadmap item.
8. **`time_branching` and `metadata_branching` step types** — typed catalog references for routing decisions.

## Patterns SolidPing should NOT copy

1. **Three units in `request_timeout`** (seconds for HTTP, ms for ports, discrete-seconds for Playwright). Use `timeoutSeconds` everywhere or split into clearly-named fields.
2. **Dual surface** for monitor channel booleans (`monitor.email/sms/call/push`) vs policy `urgency_id`. Pick one — severities only.
3. **Separate semantics for `maintenance_*` between monitor and heartbeat** ("don't check" vs "don't alert"). Pick one explicit semantic.
4. **AI incident silencing as v1**. Build deterministic primitives first (snooze duration, `incidentRouting: "page" | "silent"` flag).
5. **Pagination quirk** — incidents endpoint defaults to 10/page while everything else is 50/page. Stay consistent.
6. **Hard-coded 3-of-N quorum**. Expose as `failQuorum` (default 3 or "majority").
7. **Region API surface limited to 4 buckets** (`us`, `eu`, `as`, `au`). SolidPing's hierarchical naming (`$continent-$region-$city`) is already better.

## Patterns SolidPing already does well

- Multi-step escalation policies with cancel-on-ack — covered by our existing escalation runtime.
- Group-incident correlation via check-group rollup — equivalent to `incident_group_id`.
- Time-bounded snooze — first-class API verb.
- Maintenance windows with explicit start/end timestamps + recurrence.
- Hierarchical region naming with wildcard matching.
- Encryption at rest with master-key envelope.
- Multi-tenant from day one (BetterStack's "team" concept retrofitted on top of single-tenant assumptions).
