import { useMemo, useRef, useState, useSyncExternalStore } from "react";

import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import {
  formatInlineAbsolute,
  formatRelativeTime,
  formatTooltipText,
  formatUtcIso,
} from "@/lib/time-ago";

// --- Shared 30s ticking clock ----------------------------------------------
//
// A "46m ago" string goes stale between re-renders. Every mounted <TimeAgo>
// needs to periodically re-render so long-lived tabs don't drift — but a
// naive per-instance `setInterval` doesn't scale to a table full of rows.
// Instead every instance subscribes to ONE shared 30s interval, ref-counted
// so the timer only runs while at least one <TimeAgo> is mounted and stops
// the moment the last one unmounts.
const tickListeners = new Set<() => void>();
let tickIntervalId: ReturnType<typeof setInterval> | null = null;
let tickValue = 0;

function subscribeTick(listener: () => void): () => void {
  tickListeners.add(listener);
  if (tickIntervalId === null) {
    tickIntervalId = setInterval(() => {
      tickValue++;
      tickListeners.forEach((l) => l());
    }, 30_000);
  }
  return () => {
    tickListeners.delete(listener);
    if (tickListeners.size === 0 && tickIntervalId !== null) {
      clearInterval(tickIntervalId);
      tickIntervalId = null;
    }
  };
}

function getTickSnapshot(): number {
  return tickValue;
}

/** Forces a re-render every 30s via the shared clock above. The returned
 * value is unused by callers — it only exists to make useSyncExternalStore
 * notice a change and re-render. */
function useTick(): void {
  useSyncExternalStore(subscribeTick, getTickSnapshot, () => 0);
}

// --- Component --------------------------------------------------------------

export interface TimeAgoProps {
  /** ISO timestamp. Callers own their own null/empty fallback (see
   * `TimeAgoOrDash` below for the common "—" case) — TimeAgo always expects
   * a real date. */
  date: string;
  /**
   * "tooltip" (default): compact relative text ("46m ago"), absolute time
   * revealed on hover/tap. Use for dense lists (incidents index, jobs).
   *
   * "inline": absolute time shown inline in the browser's LOCAL time
   * ("11:31:07 CEST · 46m ago") instead of hidden behind hover — for pages
   * where the operator compares several timestamps at once (incident
   * detail: timeline, comments, header). UTC stays one step away, in the
   * hover tooltip and the click-to-copy payload.
   */
  variant?: "tooltip" | "inline";
  className?: string;
  "data-testid"?: string;
}

const COPIED_RESET_MS = 1500;
// There is no hover on touch devices, so a tap is the only way a mobile user
// ever sees the tooltip — force it open for a few seconds after a tap
// instead of relying on a hover state that will never fire there.
const TOUCH_TOOLTIP_OPEN_MS = 2500;

function isTouchDevice(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(hover: none)").matches
  );
}

/**
 * Relative timestamp with hover/tap-revealed absolute time (local + UTC) and
 * click-to-copy (ISO 8601 UTC). See `web/dash0/src/routes/orgs/$org/
 * design-reference.tsx` for both variants live, or the Proposal in
 * specs/todos/2026-08-14-03-incident-timestamps-hover-copy.md for the full
 * behavior spec.
 */
export function TimeAgo({
  date,
  variant = "tooltip",
  className,
  "data-testid": testId,
}: TimeAgoProps) {
  useTick();
  const d = useMemo(() => new Date(date), [date]);
  const [copied, setCopied] = useState(false);
  const [open, setOpen] = useState(false);
  const copiedTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const openTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const relative = formatRelativeTime(d);
  const utcIso = formatUtcIso(d);
  const tooltipText = formatTooltipText(d);
  const displayText =
    variant === "inline" ? `${formatInlineAbsolute(d)} · ${relative}` : relative;

  const handleActivate = (e: { preventDefault(): void; stopPropagation(): void }) => {
    // The incidents-list and jobs-list timestamps live inside the row's
    // <Link> — without these, "copying" the timestamp would also navigate
    // away from the row.
    e.preventDefault();
    e.stopPropagation();

    void navigator.clipboard
      .writeText(utcIso)
      .then(() => {
        setCopied(true);
        if (copiedTimeoutRef.current) clearTimeout(copiedTimeoutRef.current);
        copiedTimeoutRef.current = setTimeout(() => setCopied(false), COPIED_RESET_MS);
      })
      .catch(() => {
        // Clipboard API requires a secure context (HTTPS/localhost); ignore
        // failures silently — the relative/absolute text is still visible.
      });

    if (isTouchDevice()) {
      setOpen(true);
      if (openTimeoutRef.current) clearTimeout(openTimeoutRef.current);
      openTimeoutRef.current = setTimeout(() => setOpen(false), TOUCH_TOOLTIP_OPEN_MS);
    }
  };

  return (
    <Tooltip open={open} onOpenChange={setOpen}>
      <TooltipTrigger asChild>
        <span
          role="button"
          tabIndex={0}
          data-testid={testId ?? "time-ago"}
          aria-label={copied ? `Copied ${utcIso}` : `Copy timestamp ${utcIso}`}
          className={cn(
            "cursor-pointer underline decoration-dotted decoration-muted-foreground/60 underline-offset-2",
            className,
          )}
          onClick={handleActivate}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === " ") handleActivate(e);
          }}
        >
          {displayText}
        </span>
      </TooltipTrigger>
      <TooltipContent data-testid="time-ago-tooltip">
        {/* On tap (mobile), the "Copied" feedback carries the full absolute
            time — that tap is the only place a touch user ever sees it. */}
        {copied ? `Copied — ${tooltipText}` : tooltipText}
      </TooltipContent>
    </Tooltip>
  );
}

/**
 * <TimeAgo> for an optional/nullable timestamp — renders `emptyLabel`
 * ("—" by default, matching the jobs pages' existing convention) instead of
 * mounting the component when there is no date yet.
 */
export function TimeAgoOrDash({
  date,
  emptyLabel = "—",
  ...props
}: { date: string | null | undefined; emptyLabel?: string } & Omit<TimeAgoProps, "date">) {
  if (!date) return <>{emptyLabel}</>;
  return <TimeAgo date={date} {...props} />;
}
