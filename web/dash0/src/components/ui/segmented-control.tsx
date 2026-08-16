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
};

export type SegmentedControlProps<T extends string> = {
  value: T;
  onValueChange: (value: T) => void;
  options: SegmentedControlOption<T>[];
  /** Accessible name for the group. */
  "aria-label"?: string;
  className?: string;
};

/**
 * Two-or-more-way view switch rendered as a raised pill on a recessed track.
 *
 * The selected segment must always be *lighter* than the track it sits on, in
 * both themes. In light that is `bg-card` (white) on `bg-muted`. In dark the
 * track has to flip to `bg-background` (0.14) because dark `--muted` (0.22) is
 * lighter than dark `--card` (0.18) — reusing `bg-muted` there would invert
 * the pill and make the *inactive* segments look selected.
 *
 * `aria-pressed` and `data-testid` are rendered on each segment button.
 */
export function SegmentedControl<T extends string>({
  value,
  onValueChange,
  options,
  className,
  ...props
}: SegmentedControlProps<T>) {
  return (
    <div
      role="group"
      aria-label={props["aria-label"]}
      className={cn(
        "inline-flex rounded-lg border bg-muted p-0.5 dark:bg-background",
        className
      )}
    >
      {options.map((option) => {
        const selected = option.value === value;
        const button = (
          <Button
            size="sm"
            variant="ghost"
            className={selected ? "bg-card shadow-sm hover:bg-card" : undefined}
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
