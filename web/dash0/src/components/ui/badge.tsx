import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

const badgeVariants = cva(
  "inline-flex items-center rounded-md border px-2 py-0.5 text-xs font-medium transition-colors focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2",
  {
    variants: {
      variant: {
        // A soft tint with colored text, like the status variants below, so a
        // badge never competes with the primary button for attention.
        default: "border-primary/25 bg-primary/10 text-primary",
        secondary:
          "border-transparent bg-secondary text-secondary-foreground hover:bg-secondary/80",
        destructive:
          "border-status-error/25 bg-status-error/15 text-status-error-foreground",
        outline: "text-foreground",
        success:
          "border-status-ok/25 bg-status-ok/15 text-status-ok-foreground",
        warning:
          "border-status-warning/30 bg-status-warning/15 text-status-warning-foreground",
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
