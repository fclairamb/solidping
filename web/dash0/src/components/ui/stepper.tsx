import { Check } from "lucide-react";
import { cn } from "@/lib/utils";

export interface StepperStep {
  label: string;
}

export interface StepperProps {
  steps: StepperStep[];
  /** 1-indexed: the step currently active. Earlier steps render as done. */
  current: number;
  className?: string;
}

/**
 * Horizontal progress stepper for multi-step wizards (e.g. guided setup
 * flows). Each step is a circle — check icon once passed, filled with its
 * number while active, outline while upcoming — joined by a connector line.
 * Wraps via `flex-wrap` on narrow viewports so labels stack under their
 * circle instead of overflowing horizontally.
 */
export function Stepper({ steps, current, className }: StepperProps) {
  return (
    <ol className={cn("flex flex-wrap items-start gap-y-3", className)}>
      {steps.map((step, i) => {
        const index = i + 1;
        const done = index < current;
        const active = index === current;
        return (
          <li
            key={step.label}
            className="flex flex-1 basis-1/2 items-center gap-2 sm:basis-0"
          >
            <div className="flex items-center gap-2">
              <span
                aria-hidden
                className={cn(
                  "flex h-7 w-7 shrink-0 items-center justify-center rounded-full border text-xs font-medium",
                  done && "border-primary bg-primary text-primary-foreground",
                  active && "border-primary text-primary",
                  !done && !active && "border-muted-foreground/30 text-muted-foreground",
                )}
              >
                {done ? <Check className="h-3.5 w-3.5" /> : index}
              </span>
              <span
                className={cn(
                  "text-sm font-medium",
                  active ? "text-foreground" : "text-muted-foreground",
                )}
                aria-current={active ? "step" : undefined}
              >
                {step.label}
              </span>
            </div>
            {index < steps.length && (
              <div
                aria-hidden
                className={cn(
                  "mx-2 hidden h-px flex-1 sm:block",
                  done ? "bg-primary" : "bg-border",
                )}
              />
            )}
          </li>
        );
      })}
    </ol>
  );
}
