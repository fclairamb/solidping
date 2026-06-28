import type { MaintenanceWindow } from "@/api/hooks";

// Client-side port of server's models.IsActiveAt. The maintenance-window API
// response carries no server-computed status, so the dashboard computes it
// locally to render the Active / Upcoming / Past badge.

export type MaintenanceStatus = "active" | "upcoming" | "past";

function step(d: Date, recurrence: string): Date {
  const n = new Date(d);
  if (recurrence === "daily") n.setDate(n.getDate() + 1);
  else if (recurrence === "weekly") n.setDate(n.getDate() + 7);
  else if (recurrence === "monthly") n.setMonth(n.getMonth() + 1);
  return n;
}

export function isActiveAt(w: MaintenanceWindow, t: Date): boolean {
  const start = new Date(w.startAt);
  const end = new Date(w.endAt);
  if (w.recurrence === "none") return t >= start && t < end;
  if (w.recurrenceEnd && t > new Date(w.recurrenceEnd)) return false;
  if (t < start) return false;
  const durationMs = end.getTime() - start.getTime();
  let cur = new Date(start);
  for (;;) {
    const next = step(cur, w.recurrence);
    if (next > t) break;
    cur = next;
  }
  return t >= cur && t < new Date(cur.getTime() + durationMs);
}

export function computeMaintenanceStatus(
  w: MaintenanceWindow,
  now = new Date(),
): MaintenanceStatus {
  if (isActiveAt(w, now)) return "active";
  if (w.recurrence === "none") {
    return now < new Date(w.startAt) ? "upcoming" : "past";
  }
  if (w.recurrenceEnd && now > new Date(w.recurrenceEnd)) return "past";
  return "upcoming";
}

// Badge variant for each status (matches the variants shipped on the Badge
// primitive — see design-reference.tsx).
export function maintenanceStatusBadgeVariant(
  status: MaintenanceStatus,
): "success" | "secondary" | "outline" {
  switch (status) {
    case "active":
      return "success";
    case "upcoming":
      return "secondary";
    case "past":
      return "outline";
  }
}
