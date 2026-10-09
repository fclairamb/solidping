import * as React from "react";
import { cn } from "@/lib/utils";

export interface ChipProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  /** Highlights the chip whose value is the current one. */
  selected?: boolean;
}

/** Small pill-shaped button: one click picks a suggested value. */
const Chip = React.forwardRef<HTMLButtonElement, ChipProps>(
  ({ className, selected = false, type = "button", ...props }, ref) => (
    <button
      ref={ref}
      type={type}
      aria-pressed={selected}
      className={cn(
        "inline-flex h-7 cursor-pointer items-center rounded-full border px-2.5 text-xs transition-colors",
        "hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        selected
          ? "border-primary bg-primary/10 text-foreground"
          : "text-muted-foreground",
        className,
      )}
      {...props}
    />
  ),
);
Chip.displayName = "Chip";

export { Chip };
