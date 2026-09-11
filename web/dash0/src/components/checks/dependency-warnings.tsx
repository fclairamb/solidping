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
        <button
          type="button"
          data-testid="dependency-warning-hint"
          aria-label={title}
          className="relative inline-flex h-3.5 w-3.5 shrink-0 items-center justify-center text-amber-600 dark:text-amber-400"
        >
          {/* Invisible ~44px hit area around the small glyph, so the tap
              target is comfortable without the icon itself growing. */}
          <span aria-hidden="true" className="absolute -inset-[15px]" />
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
