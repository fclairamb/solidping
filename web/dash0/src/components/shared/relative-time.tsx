import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

// formatDuration renders a millisecond duration as a compact, unit-capped
// human string: "45s", "3m 12s", "2h 5m", "3d 4h 5m". Shared by every
// live-ticking "ago"/"for" display in the app (check summary cards, private
// location agents, …) so there is a single place tuning the cutoffs.
export function formatDuration(ms: number): string {
  const seconds = Math.floor(ms / 1000);
  const minutes = Math.floor(seconds / 60);
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);

  if (days > 0) return `${days}d ${hours % 24}h ${minutes % 60}m`;
  if (hours > 0) return `${hours}h ${minutes % 60}m`;
  if (minutes > 0) return `${minutes}m ${seconds % 60}s`;
  return `${seconds}s`;
}

/** Live-ticking elapsed time since `since`, formatted with formatDuration.
 * Ticks via its own 1s interval (cleared on unmount) — mount it once per
 * logical subject (e.g. one instance per table row) rather than
 * recreating it on every parent re-render, or the ticker resets. */
export function LiveDuration({ since }: { since: string }) {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const interval = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(interval);
  }, []);

  const elapsed = now - new Date(since).getTime();
  return <>{formatDuration(Math.max(0, elapsed))}</>;
}

// LiveDurationAgo wraps the live elapsed time in the locale-appropriate
// "ago" template ("5m ago" in EN, "il y a 5m" in FR, "vor 5m" in DE/ES) so
// the suffix isn't hard-coded as a trailing word. Reuses the "checks"
// namespace's detail.summary.ago key (translated in all four locales)
// rather than duplicating a near-identical key per feature namespace.
export function LiveDurationAgo({ since }: { since: string }) {
  const { t } = useTranslation("checks");
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const interval = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(interval);
  }, []);

  const elapsed = Math.max(0, now - new Date(since).getTime());
  return <>{t("detail.summary.ago", { duration: formatDuration(elapsed) })}</>;
}

// formatDurationCoarse renders only the largest significant unit: "45m",
// "3h", "2d". formatDuration's second unit ("45m 50s") is precision a reader
// scanning a log cannot use, and it makes a column of timestamps ragged —
// here the whole point is that they line up and can be skimmed.
export function formatDurationCoarse(ms: number): string {
  const seconds = Math.floor(ms / 1000);
  const minutes = Math.floor(seconds / 60);
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);

  if (days > 0) return `${days}d`;
  if (hours > 0) return `${hours}h`;
  if (minutes > 0) return `${minutes}m`;
  return `${seconds}s`;
}

// DurationAgo is the non-ticking sibling of LiveDurationAgo, for historical
// rows where the value will not meaningfully change while it is on screen —
// an audit log, a resolved incident. Long lists are exactly where the live
// variant hurts: one 1s interval per row adds up for no benefit, since a
// second of drift on a week-old event is invisible. Shares the same
// "checks:detail.summary.ago" template so both read identically.
export function DurationAgo({ since }: { since: string }) {
  const { t } = useTranslation("checks");
  // Read the clock in a state initializer rather than during render: Date.now()
  // is impure, and re-reading it on every re-render would let the same row
  // silently change value. Sampled once per mount is exactly right here —
  // the underlying event is historical and does not move.
  const [now] = useState(() => Date.now());
  const elapsed = Math.max(0, now - new Date(since).getTime());
  return (
    <>{t("detail.summary.ago", { duration: formatDurationCoarse(elapsed) })}</>
  );
}
