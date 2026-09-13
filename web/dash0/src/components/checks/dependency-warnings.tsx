import { useTranslation } from "react-i18next";
import { Info } from "lucide-react";

import type { DependencyWarning } from "@/api/hooks";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { resolveCheckRefLabel } from "@/lib/dependency-graph";

interface DependencyWarningHintProps {
  warning: DependencyWarning;
}

/**
 * The soft configuration lint on a check's hard `dependsOn` edges
 * (spec 2026-08-31-06), rendered inline on the "Depends on" row it concerns
 * instead of a full-width banner stacked above the list (spec
 * 2026-09-10-02) — a check with five hard parents that share the lint used
 * to produce five near-identical amber boxes before the reader ever reached
 * the row they described.
 *
 * Amber, never destructive, and never a blocking validation: the edge is
 * legal, the runtime confirmation hold already covers the gap at page time,
 * and the only consequence is that a page may arrive later than this
 * check's configured confirmation suggests.
 *
 * A `Popover` rather than a `Tooltip`: the explanation must open on tap, not
 * just hover, for the page to stay usable on touch. Radix's Popover trigger
 * is click/tap-driven on every input type, so one primitive covers both.
 */
export function DependencyWarningHint({ warning }: DependencyWarningHintProps) {
  const { t } = useTranslation(["dependencies"]);
  const parent =
    resolveCheckRefLabel(warning.parentCheck) || t("dependencies:unknownCheck");
  const title = t("dependencies:warnings.confirmationMargin.title", { parent });
  const body = t("dependencies:warnings.confirmationMargin.body", {
    parent,
    current: warning.childConfirmationSeconds,
    recommended: warning.recommendedConfirmationSeconds,
  });

  return (
    <Popover>
      <PopoverTrigger asChild>
        {/*
         * The ~44px touch target is REAL padding around the glyph, not an
         * absolutely-positioned overlay: DependencyRowList clips its
         * children with overflow-hidden (so a hover fill never pokes past
         * the container's rounded corners), and that clip applies just as
         * much to a negative-margin or absolutely-positioned hit area as to
         * any other painted content — an overlay would get sliced flush at
         * the list's top/bottom edge, silently shrinking the target on the
         * first and last row. Real padding instead grows this button's own
         * box in normal flow, which the row's `items-center` flex simply
         * makes room for (min-h-10 is a floor, not a cap) — nothing
         * overflows the list, so nothing gets clipped, on any row.
         */}
        <button
          type="button"
          data-testid="dependency-warning-hint"
          aria-label={title}
          className="inline-flex shrink-0 items-center justify-center rounded-full p-[15px] text-amber-600 transition-colors hover:bg-amber-500/10 dark:text-amber-400"
        >
          <Info className="h-3.5 w-3.5" />
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-72 space-y-1 p-3">
        <p className="text-sm font-medium">{title}</p>
        <p className="text-xs text-muted-foreground">{body}</p>
      </PopoverContent>
    </Popover>
  );
}
