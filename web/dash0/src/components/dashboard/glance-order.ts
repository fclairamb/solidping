import type { Check } from "@/api/hooks";

// Max rows in the "Checks at a glance" card. The footer always links to the
// full /checks list so a capped card is never mistaken for the whole fleet.
const GLANCE_LIMIT = 10;

export function effectiveStatus(check: Check): string | undefined {
  return check.status ?? check.lastResult?.status;
}

// The down / hard-down predicates that used to live here moved server-side to
// server/internal/handlers/checks/stats.go (isDownWireStatus /
// isHardDownWireStatus), where they are applied to the whole fleet instead of
// one page. Keeping a second copy here would let the two drift apart, which is
// exactly the failure mode the stats endpoint exists to prevent.

export function isAttentionStatus(status?: string): boolean {
  return !!status && status !== "up" && status !== "created";
}

// Ordering for the glance card: enabled checks needing attention first (status
// not up/created, most recent lastStatusChange first), then healthy enabled
// checks by name, then disabled checks last. A disabled check keeps its last
// status, so a paused "down" check is not an attention item
// (solidping-business#17). Capped to GLANCE_LIMIT rows.
export function orderChecksForGlance(checks: Check[]): Check[] {
  const byName = (a: Check, b: Check) =>
    (a.name || a.slug || a.uid).localeCompare(b.name || b.slug || b.uid);

  const needsAttention = (c: Check) =>
    c.enabled !== false && isAttentionStatus(effectiveStatus(c));

  const attention = checks
    .filter(needsAttention)
    .sort((a, b) => {
      const aTime = a.lastStatusChange?.time
        ? new Date(a.lastStatusChange.time).getTime()
        : 0;
      const bTime = b.lastStatusChange?.time
        ? new Date(b.lastStatusChange.time).getTime()
        : 0;
      return bTime - aTime;
    });

  const rest = checks.filter((c) => !needsAttention(c));
  const healthyEnabled = rest.filter((c) => c.enabled !== false).sort(byName);
  const disabled = rest.filter((c) => c.enabled === false).sort(byName);

  return [...attention, ...healthyEnabled, ...disabled].slice(0, GLANCE_LIMIT);
}
