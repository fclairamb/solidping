import * as React from "react";
import { ChevronDown } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { cn } from "@/lib/utils";

export interface SecondaryDisclosureCardProps {
  /** Section heading. Rendered as a real <h2>, always visible. */
  title: React.ReactNode;
  /** One line saying what is inside, shown under the heading. */
  description?: React.ReactNode;
  /** Trigger label while collapsed — say what opening it does. */
  expandLabel: string;
  /** Trigger label while open. */
  collapseLabel: string;
  /** Start expanded. Default false: the point of this card is to de-emphasize. */
  defaultOpen?: boolean;
  className?: string;
  /** data-testid applied to the expand/collapse trigger. */
  "data-testid"?: string;
  children: React.ReactNode;
}

/**
 * An outlined card whose heading and description stay visible while its body is
 * collapsed behind a trigger — the "there is a primary action above, and this is
 * the other, rarer option" treatment.
 *
 * Use it when two actions used to sit side by side with equal weight and one of
 * them should now clearly be the default (the /no-org screen: create your own
 * organization first and full-width, join an existing one below).
 *
 * Deliberately NOT `CollapsibleSection`: that one is a progressive-disclosure
 * row for long forms, and it puts its title INSIDE the trigger button, where
 * assistive tech (and role-based tests) can no longer see a heading. Here the
 * heading is a sibling of the trigger, so the section stays announced and
 * findable whether it is open or shut.
 */
export function SecondaryDisclosureCard({
  title,
  description,
  expandLabel,
  collapseLabel,
  defaultOpen = false,
  className,
  "data-testid": testId,
  children,
}: SecondaryDisclosureCardProps) {
  const [open, setOpen] = React.useState(defaultOpen);

  return (
    <Collapsible
      open={open}
      onOpenChange={setOpen}
      className={cn("rounded-lg border bg-card/50", className)}
    >
      <div className="flex flex-col gap-2 px-4 py-3 sm:flex-row sm:items-center sm:justify-between sm:gap-3">
        <div className="min-w-0">
          <h2 className="text-sm font-medium text-muted-foreground">{title}</h2>
          {description ? (
            <p className="text-xs text-muted-foreground/80">{description}</p>
          ) : null}
        </div>
        <CollapsibleTrigger asChild>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            data-testid={testId}
            className="shrink-0 self-start text-muted-foreground sm:self-auto"
          >
            {open ? collapseLabel : expandLabel}
            <ChevronDown
              className={cn(
                "ml-2 h-4 w-4 transition-transform",
                open && "rotate-180",
              )}
            />
          </Button>
        </CollapsibleTrigger>
      </div>
      <CollapsibleContent className="space-y-3 px-4 pb-4">
        {children}
      </CollapsibleContent>
    </Collapsible>
  );
}
