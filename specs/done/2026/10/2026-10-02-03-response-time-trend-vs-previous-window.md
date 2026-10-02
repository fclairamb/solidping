# Response time: show the trend against the previous window

## Problem

The user request behind this was not really "show me a number". It was: "it
would be helpful just to see like, okay, it actually got slower or faster over a
certain time period, versus just looking at the graph and trying to eyeball
where approximately it is."

A single average answers "how fast is it". It does not answer "is it getting
worse", which is the question. Answering that by eye on the chart is exactly
what the user said he was doing and did not want to do.

Nothing on the check detail page compares two windows. The stats strip
([`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:1960`](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:1960))
describes the selected window only.

## Proposal

Next to the average in the stats strip, show the change against the immediately
preceding window of the same length: 24 h selected means compare with the 24 h
before it.

- **One number, with a direction and a sign**: `avg 142 ms (+18% vs previous
  24h)`. Slower is bad, so colour by direction, not by magnitude, and keep the
  existing status palette rather than inventing one.
- **Compare avg to avg.** Not p95 to p95 — the p95 combination across rollup
  rows is an approximation
  ([`calculateAggregatedMetrics`](server/internal/jobs/jobtypes/job_aggregation.go:1216)),
  and a ratio of two approximations is noise. Revisit once
  `2026-10-02-02-store-duration-p50.md` lands: a p50 ratio is the better signal
  and a plausible follow-up.
- **The previous window costs a second query.** The chart window query is
  already bounded by tier
  ([the raw/rollup split guard](server/internal/db/models/result_bucket.go:16)),
  so the comparison window has to be fetched the same way and may land on the
  other side of the split. Fetch per side, never one query naming both.
  Preferably ask the availability/bucket aggregate for two numbers rather than
  re-shipping rows: a window average is one `avg()`, not 1 337 rows.
- **Say nothing rather than something wrong.** No comparison when the previous
  window has no data, has an order of magnitude fewer samples, or overlaps a
  maintenance window. A check created yesterday must not read `+900%`.
- **Suppress noise.** Below some threshold (5%?) render "flat" instead of a
  signed percentage, so a stable check does not flicker between +1% and -1% on
  every refetch.

## Open questions

- Previous window, or a longer baseline? "vs the previous 24h" is noisy on a
  single bad hour; "vs the 7-day average" is steadier but answers a slightly
  different question. The user asked about a period getting slower, which is the
  former. Ship the former, and only add the latter if someone asks.
- Does this belong in the strip, or as a second (ghosted) series on the chart
  showing the previous window overlaid? The overlay is prettier and much more
  work; the number is what was actually asked for.
- Maintenance windows are already excluded from availability but not from
  duration aggregates. Check what the existing avg does with them before
  deciding what the comparison does.

## Resolved open questions

- Compare against the previous window of equal length, not a longer baseline.
- Show it as a number in the stats strip, not as a ghosted overlay series on the chart.
- Maintenance windows: mirror whatever the existing avg does with them (check it), so the comparison is consistent with the strip. Record the finding in a comment.

## Not in scope

Alerting on a trend — that is what SLOs and burn-rate alerting are for
(`specs/done/2026/08/2026-08-21-08-slo-burn-rate-alerting.md`). This is a
read-only number on a page.
