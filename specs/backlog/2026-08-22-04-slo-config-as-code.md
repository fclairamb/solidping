# Config-as-code for SLOs (and their burn-rate alert policies)

> **Carved out of `2026-08-21-08-slo-burn-rate-alerting` on 2026-08-22.** That
> spec asked for "policies export/import with the SLO" as a one-line item. The
> implementation audit found the premise doesn't hold: there is no SLO
> export/import surface to extend, so the item is really a feature of its own.

## Problem

Config-as-code today is **checks-only**:
[`server/internal/handlers/checks/export_v2.go`](server/internal/handlers/checks/export_v2.go)
and `checks/apply.go`, exposed at `/checks/export` and `/checks/import`
([`server/internal/app/server.go:933`](server/internal/app/server.go:933)), and
[`wiki/features/config-as-code.md`](wiki/features/config-as-code.md) is titled
"Config-as-code (declarative checks)" with zero SLO mentions.

So an org that manages its checks declaratively still has to click its SLOs —
and now its burn-rate alert policies — into existence by hand. The two halves
of the same monitoring config live in different worlds.

## Proposal

Extend the config-as-code document format to cover SLOs, and carry their alert
policies inside the SLO document rather than as a separate top-level object
(the policies have no meaning without their SLO, and `slo_alert_policies` rows
are already lifecycle-bound to one).

Sketch:

- A `slos:` section alongside the existing `checks:`, keyed by SLO slug/name,
  referencing checks or groups by the same identifiers the checks section uses.
- Each SLO carries an optional `alerting:` block — one entry per policy kind
  (`fast` / `slow`), each with `enabled`, `threshold`, `longWindow`,
  `shortWindow`, `severity`, `minSamples`. Omitted policies keep the built-in
  defaults from `DefaultSLOAlertPolicies()`
  ([`server/internal/db/models/slo_alert_policy.go:116`](server/internal/db/models/slo_alert_policy.go:116)).
- Import is declarative in the same sense the checks importer is: absent means
  "leave alone" or "delete", following whatever the checks importer already
  does — do not invent a second reconciliation semantic.
- Export round-trips: exporting an org and re-importing it must be a no-op.

## Open questions

- Does the checks importer's prune/no-prune semantic extend cleanly to SLOs, or
  do SLOs need their own flag? Decide by reading `checks/apply.go` first.
- Group-scoped SLOs reference a group; groups are not currently part of the
  export format either. That may need to come first, or SLOs may have to
  reference groups by name and fail loudly when the group is absent.

## Not in scope

The burn-rate alerting feature itself — that shipped in
`specs/done/2026/08/2026-08-21-08-slo-burn-rate-alerting.md`. This spec only
adds the declarative surface for configuring it.
