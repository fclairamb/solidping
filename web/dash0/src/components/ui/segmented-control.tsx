import * as React from "react";

import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

export type SegmentedControlOption<T extends string> = {
  value: T;
  label: React.ReactNode;
  /** Optional tooltip shown on hover/focus of this segment. */
  tooltip?: React.ReactNode;
  /** Forwarded verbatim — E2E asserts on it. */
  testId?: string;
  /** Accessible name for this segment, needed when `label` is icon-only. */
  ariaLabel?: string;
};

export type SegmentedControlProps<T extends string> = {
  value: T;
  onValueChange: (value: T) => void;
  options: SegmentedControlOption<T>[];
  /** Accessible name for the group. */
  "aria-label"?: string;
  className?: string;
  /**
   * "sm" (default) keeps the compact control. "md" renders the whole control
   * 36px high, the same as a default Button, so it lines up with the buttons
   * and inputs of a toolbar (spec 2026-09-30-02).
   */
  size?: "sm" | "md";
};

/**
 * Two-or-more-way view switch: one recessed track, the selected segment in the
 * primary color (a `default` Button, like a selected chart range), the others
 * `ghost`. A neutral raised pill was too close to its track to tell apart in
 * dark mode.
 *
 * The track is `bg-muted`, flipped to `bg-background` in dark where `--muted`
 * is lighter than `--card`.
 *
 * `aria-pressed` and `data-testid` are rendered on each segment button.
 */
export function SegmentedControl<T extends string>({
  value,
  onValueChange,
  options,
  className,
  size = "sm",
  ...props
}: SegmentedControlProps<T>) {
  return (
    <div
      role="group"
      aria-label={props["aria-label"]}
      className={cn(
        "inline-flex rounded-lg border bg-muted p-0.5 dark:bg-background",
        size === "md" ? "h-9 flex-nowrap" : "flex-wrap",
        className
      )}
    >
      {options.map((option) => {
        const selected = option.value === value;
        const button = (
          <Button
            size="sm"
            variant={selected ? "default" : "ghost"}
            className={cn(size === "md" && "h-[1.875rem]")}
            aria-label={option.ariaLabel}
            onClick={() => onValueChange(option.value)}
            aria-pressed={selected}
            data-testid={option.testId}
          >
            {option.label}
          </Button>
        );

        if (!option.tooltip) {
          return <React.Fragment key={option.value}>{button}</React.Fragment>;
        }

        return (
          <Tooltip key={option.value}>
            <TooltipTrigger asChild>{button}</TooltipTrigger>
            <TooltipContent>{option.tooltip}</TooltipContent>
          </Tooltip>
        );
      })}
    </div>
  );
}
