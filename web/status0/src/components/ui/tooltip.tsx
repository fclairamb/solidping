import * as React from "react";
import * as TooltipPrimitive from "@radix-ui/react-tooltip";

import { cn } from "@/lib/utils";

const TooltipProvider = TooltipPrimitive.Provider;

const Tooltip = TooltipPrimitive.Root;

const TooltipTrigger = TooltipPrimitive.Trigger;

/**
 * Tooltips render on the *popover* surface, not on `--primary`.
 *
 * The old shadcn default painted a solid brand-blue slab with white text; on a
 * status page — where color is meaningful and reserved for up/degraded/down —
 * that slab competed with the status colors it was floating over and read as a
 * heavy, muddy blob. A bordered popover surface with a soft shadow keeps the
 * tooltip legible in both themes while leaving color free to mean status.
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
