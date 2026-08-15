import * as React from "react";
import * as TooltipPrimitive from "@radix-ui/react-tooltip";

import { cn } from "@/lib/utils";

const TooltipProvider = TooltipPrimitive.Provider;

const Tooltip = TooltipPrimitive.Root;

const TooltipTrigger = TooltipPrimitive.Trigger;

/**
 * Tooltips render on the *popover* surface, not on `--primary`.
 *
 * The old shadcn default painted a solid brand-blue slab with white text. Next
 * to the status colors this console uses everywhere (green/amber/red rows,
 * charts, badges) that slab read as a heavy, muddy block and fought with the
 * data underneath it. A bordered popover surface with a soft shadow stays
 * legible in both themes and leaves color free to mean status.
 *
 * Kept in sync with status0's copy (web/status0/src/components/ui/tooltip.tsx).
 *
 * No `overflow-hidden`: the arrow is positioned outside the content box and
 * would be clipped by it.
 */
const TooltipContent = React.forwardRef<
  React.ComponentRef<typeof TooltipPrimitive.Content>,
  React.ComponentPropsWithoutRef<typeof TooltipPrimitive.Content> & {
    /** Set false to drop the pointer (e.g. when the trigger is tiny). */
    arrow?: boolean;
  }
>(({ className, sideOffset = 6, arrow = true, children, ...props }, ref) => (
  <TooltipPrimitive.Portal>
    <TooltipPrimitive.Content
      ref={ref}
      sideOffset={sideOffset}
      className={cn(
        "z-50 max-w-xs rounded-lg border border-border bg-popover/95 px-2.5 py-1.5 text-xs text-popover-foreground shadow-lg backdrop-blur-sm",
        "animate-in fade-in-0 zoom-in-95 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95 data-[side=bottom]:slide-in-from-top-1 data-[side=left]:slide-in-from-right-1 data-[side=right]:slide-in-from-left-1 data-[side=top]:slide-in-from-bottom-1",
        className
      )}
      {...props}
    >
      {children}
      {arrow && (
        <TooltipPrimitive.Arrow
          width={11}
          height={5}
          className="fill-popover drop-shadow-[0_1px_0_var(--border)]"
        />
      )}
    </TooltipPrimitive.Content>
  </TooltipPrimitive.Portal>
));
TooltipContent.displayName = TooltipPrimitive.Content.displayName;

export { Tooltip, TooltipTrigger, TooltipContent, TooltipProvider };
