import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { DependencyKind } from "@/api/hooks";
import { cn } from "@/lib/utils";

// Dependency rows are a *list surface*, not a pile of separately-outlined
// boxes: one bordered container, `divide-y` between rows, and a background a
// step off the card/section behind it so the list reads as one object. See the
// "Dependency row" section of the design reference
// (routes/orgs/$org/design-reference.tsx) for the canonical rendering.

interface DependencyRowListProps {
  /**
   * `muted` (default) contrasts against a `bg-card` panel — the check detail
   * page. `card` is for a container that already sits on a muted surface.
   */
  tone?: "muted" | "card";
  className?: string;
  children: ReactNode;
  "data-testid"?: string;
}

export function DependencyRowList({
  tone = "muted",
  className,
  children,
  "data-testid": testId,
}: DependencyRowListProps) {
  return (
    <div
      data-testid={testId}
      className={cn(
        "divide-y overflow-hidden rounded-lg border",
        tone === "muted" ? "bg-muted/30" : "bg-card",
        className,
      )}
    >
      {children}
    </div>
  );
}

interface DependencyRowProps {
  /** The check this edge points at — a link on read-only surfaces. */
  identity: ReactNode;
  /** Kind badge (read-only) or kind select (edit form). */
  kind?: ReactNode;
  /** Free-text description, or the description input on the edit form. */
  description?: ReactNode;
  /** Trailing actions — remove, etc. */
  actions?: ReactNode;
  /** Tint on hover, for rows that behave as links. */
  interactive?: boolean;
  className?: string;
  "data-testid"?: string;
}

// DependencyRow lays out one edge in fixed columns: identity · kind ·
// description · actions. Below `sm` the grid collapses to a single column so
// the description wraps under the identity instead of squeezing it; the row
// keeps a 40px minimum height so every control stays a comfortable touch
// target.
export function DependencyRow({
  identity,
  kind,
  description,
  actions,
  interactive = false,
  className,
  "data-testid": testId,
}: DependencyRowProps) {
  return (
    <div
      data-testid={testId}
      className={cn(
        "flex min-h-10 items-center gap-3 px-3 py-2",
        interactive && "transition-colors hover:bg-muted/50",
        className,
      )}
    >
      <div className="grid min-w-0 flex-1 items-center gap-x-3 gap-y-1 sm:grid-cols-[minmax(0,12rem)_auto_minmax(0,1fr)]">
        <div className="min-w-0 truncate text-sm font-medium">{identity}</div>
        <div className="min-w-0">{kind}</div>
        <div className="min-w-0">{description}</div>
      </div>
      {actions ? (
        <div className="flex shrink-0 items-center gap-1">{actions}</div>
      ) : null}
    </div>
  );
}

// DependencyRowText renders a read-only description cell: muted, truncated,
// with the full text available as a title tooltip.
export function DependencyRowText({ children }: { children: string }) {
  return (
    <span
      className="block truncate text-sm text-muted-foreground"
      title={children}
    >
      {children}
    </span>
  );
}

// DependencyEmptyRow keeps an empty list inside the same bordered container
// rather than dropping to a bare paragraph under the heading.
export function DependencyEmptyRow({ children }: { children: ReactNode }) {
  return (
    <div className="flex min-h-10 items-center px-3 py-2 text-sm text-muted-foreground">
      {children}
    </div>
  );
}

// DependencyKindBadge is a small dot-pill, matching the "customized" marker on
// CollapsibleSection: red for a hard edge (suppresses paging), blue for soft
// (informational only).
export function DependencyKindBadge({ kind }: { kind: DependencyKind }) {
  const { t } = useTranslation(["dependencies"]);
  return (
    <span
      data-testid={`dependency-kind-${kind}`}
      className={cn(
        "inline-flex items-center gap-1 rounded-full px-1.5 py-0.5 text-[10px] font-medium",
        kind === "hard"
          ? "bg-red-500/10 text-red-600 dark:text-red-400"
          : "bg-blue-500/10 text-blue-600 dark:text-blue-400",
      )}
    >
      <span className="h-1.5 w-1.5 rounded-full bg-current" />
      {kind === "hard"
        ? t("dependencies:kindHard")
        : t("dependencies:kindSoft")}
    </span>
  );
}
