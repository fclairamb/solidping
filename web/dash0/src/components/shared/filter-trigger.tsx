import * as React from "react";
import { CirclePlus } from "lucide-react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export interface FilterTriggerProps
  extends Omit<React.ButtonHTMLAttributes<HTMLButtonElement>, "title"> {
  /** The dimension name: "Status", "Type", "Labels", "Scope". */
  title: string;
  /**
   * Value badges shown after the name (already collapsed by the caller, see
   * `facetedFilterBadges`). Empty means the filter is inactive.
   */
  badges?: string[];
  /**
   * Below `sm` the trigger shows this count instead of the badges, so a strip
   * of triggers stays narrow. Omit for filters whose badge is already short.
   */
  count?: number;
  /** Force the active look when there are no value badges (e.g. a count). */
  active?: boolean;
}

const BADGE_CLASS =
  "rounded-md bg-accent px-1.5 py-0.5 text-xs font-semibold text-accent-foreground";

/**
 * The one look every checks-list filter trigger shares (spec 2026-09-30-02).
 * Inactive: dashed outline, `⊕ Name`. Active: solid outline, the name, a 1px
 * separator, then accent value badges. Forwards its ref and props so it works
 * as a Radix `asChild` trigger for a Popover or a DropdownMenu.
 */
export const FilterTrigger = React.forwardRef<HTMLButtonElement, FilterTriggerProps>(
  ({ title, badges = [], count, active, className, ...props }, ref) => {
    const isActive = active ?? badges.length > 0;
    return (
      <Button
        ref={ref}
        type="button"
        variant="outline"
        data-active={isActive ? "true" : "false"}
        className={cn("shrink-0 font-normal", !isActive && "border-dashed", className)}
        {...props}
      >
        <CirclePlus className="h-4 w-4 text-muted-foreground" />
        <span>{title}</span>
        {isActive && badges.length > 0 && (
          <>
            <span aria-hidden className="h-4 w-px bg-border" />
            <span className="hidden items-center gap-1 sm:flex" data-testid="filter-trigger-badges">
              {badges.map((badge) => (
                <span key={badge} className={BADGE_CLASS}>
                  {badge}
                </span>
              ))}
            </span>
            <span className={cn(BADGE_CLASS, "sm:hidden")}>{count ?? badges.length}</span>
          </>
        )}
      </Button>
    );
  },
);
FilterTrigger.displayName = "FilterTrigger";

/** The accent value-badge look, for chips that sit next to a trigger. */
export const FILTER_VALUE_BADGE_CLASS = BADGE_CLASS;
