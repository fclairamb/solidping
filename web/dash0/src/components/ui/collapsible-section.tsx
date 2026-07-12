import * as React from "react";
import { ChevronDown } from "lucide-react";
import { cn } from "@/lib/utils";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";

export interface CollapsibleSectionProps {
  /** DOM id on the section root — used for `?section=` deep-links and scroll. */
  id?: string;
  title: React.ReactNode;
  /**
   * A one-line summary of the section's current effective state, shown next to
   * the title while collapsed (e.g. "confirm 120s, recover 120s"). Keeps
   * collapsed sections honest: nothing is hidden, only compressed.
   */
  summary?: React.ReactNode;
  /** Render the "customized" marker when the section deviates from defaults. */
  customized?: boolean;
  /** Initial open state — drive from content (open if it holds non-defaults). */
  defaultOpen?: boolean;
  /**
   * Monotonic signal that force-expands the section and scrolls it into view
   * whenever it changes to a truthy value. The form bumps this for a section
   * that owns a validation error on submit, and for a `?section=` deep-link.
   */
  expandSignal?: number;
  className?: string;
  children: React.ReactNode;
  /** data-testid applied to the header trigger button. */
  "data-testid"?: string;
}

export function CollapsibleSection({
  id,
  title,
  summary,
  customized = false,
  defaultOpen = false,
  expandSignal = 0,
  className,
  children,
  "data-testid": testId,
}: CollapsibleSectionProps) {
  const [open, setOpen] = React.useState(defaultOpen);
  const rootRef = React.useRef<HTMLDivElement>(null);
  const lastSignal = React.useRef(expandSignal);
  const prevDefaultOpen = React.useRef(defaultOpen);

  React.useEffect(() => {
    if (expandSignal && expandSignal !== lastSignal.current) {
      lastSignal.current = expandSignal;
      setOpen(true);
      rootRef.current?.scrollIntoView({ behavior: "smooth", block: "start" });
    }
  }, [expandSignal]);

  // Open (without scrolling) when the "holds non-default values" signal flips
  // true after mount — e.g. an edit page whose dependencies load asynchronously.
  React.useEffect(() => {
    if (defaultOpen && !prevDefaultOpen.current) {
      setOpen(true);
    }
    prevDefaultOpen.current = defaultOpen;
  }, [defaultOpen]);

  return (
    <Collapsible
      ref={rootRef}
      id={id}
      open={open}
      onOpenChange={setOpen}
      className={cn("scroll-mt-4 rounded-lg border bg-card", className)}
    >
      <CollapsibleTrigger
        type="button"
        data-testid={testId}
        className="flex w-full items-center justify-between gap-3 rounded-lg px-4 py-3 text-left outline-none transition-colors hover:bg-muted/40 focus-visible:ring-2 focus-visible:ring-ring"
      >
        <div className="min-w-0 space-y-0.5">
          <div className="flex items-center gap-2 text-sm font-medium">
            <span>{title}</span>
            {customized ? (
              <span
                data-testid="section-customized-badge"
                className="inline-flex items-center gap-1 rounded-full bg-primary/10 px-1.5 py-0.5 text-[10px] font-medium text-primary"
              >
                <span className="h-1.5 w-1.5 rounded-full bg-primary" />
                customized
              </span>
            ) : null}
          </div>
          {summary && !open ? (
            <p className="truncate text-xs text-muted-foreground">{summary}</p>
          ) : null}
        </div>
        <ChevronDown
          className={cn(
            "h-4 w-4 shrink-0 text-muted-foreground transition-transform",
            open && "rotate-180",
          )}
        />
      </CollapsibleTrigger>
      <CollapsibleContent className="space-y-4 px-4 pb-4 pt-1">
        {children}
      </CollapsibleContent>
    </Collapsible>
  );
}
