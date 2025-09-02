import { cn } from "@/lib/utils";
import { HoverCard, HoverCardContent, HoverCardTrigger } from "./hover-card";

type StatusHoverCardProps = React.ComponentProps<typeof HoverCardTrigger> & {
  title: string;
  description: string;
};

export function StatusHoverCard({
  title,
  description,
  children,
  ...props
}: StatusHoverCardProps) {
  return (
    <HoverCard openDelay={200}>
      <HoverCardTrigger {...props} className="group">
        {children}
      </HoverCardTrigger>
      <HoverCardContent className="p-0">
        <div
          className={cn(
            "border-b p-2 text-sm font-medium",
            "group-data-[status='ok']:bg-teal-500/10 group-data-[status='ok']:text-teal-500",
            "group-data-[status='error']:bg-rose-500/10 group-data-[status='error']:text-rose-500",
            "group-data-[status='warning']:bg-amber-400/10 group-data-[status='warning']:text-amber-400",
          )}
        >
          {title}
        </div>
        <div className="p-2 text-sm text-muted-foreground">{description}</div>
      </HoverCardContent>
    </HoverCard>
  );
}
