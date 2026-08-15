import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

const badgeVariants = cva(
  "inline-flex items-center rounded-md border px-2.5 py-0.5 text-xs font-semibold transition-colors focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2",
  {
    variants: {
      variant: {
        default:
          "border-transparent bg-primary text-primary-foreground shadow hover:bg-primary/80",
        secondary:
          "border-transparent bg-secondary text-secondary-foreground hover:bg-secondary/80",
        // Status variants use the soft-tint recipe shared with dash0
        // (web/dash0/src/components/ui/badge.tsx): a 15% fill of the status
        // hue, a 25% border, and the darkened `-foreground` tone for the text.
        // Replaces the previous saturated `bg-green-500`-style blocks, which
        // shouted louder than the availability bars they sat next to and had
        // no dark-mode story.
        destructive:
          "border-status-error/25 bg-status-error/15 text-status-error-foreground",
        outline: "text-foreground",
        success:
          "border-status-ok/25 bg-status-ok/15 text-status-ok-foreground",
        warning:
          "border-status-warning/30 bg-status-warning/15 text-status-warning-foreground",
        // status0-only: the page-level "under maintenance" rollup. Blue rather
        // than amber — maintenance is planned, not a fault.
        info: "border-primary/25 bg-primary/15 text-primary",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  }
);

export interface BadgeProps
  extends React.HTMLAttributes<HTMLDivElement>,
    VariantProps<typeof badgeVariants> {}

function Badge({ className, variant, ...props }: BadgeProps) {
  return (
    <div className={cn(badgeVariants({ variant }), className)} {...props} />
  );
}

export { Badge, badgeVariants };
