import { AnimatePresence, motion } from "motion/react";
import { cn } from "@/lib/utils";
import { Wifi, WifiHigh, WifiOff } from "lucide-react";
import { Badge } from "./badge";
import { StatusHoverCard } from "./status-hover-card";

export function ServiceTilePreview({
  className,
  ...props
}: React.ComponentProps<"div">) {
  return (
    <div
      {...props}
      className={cn("flex items-center gap-3 h-[48px]", className)}
    />
  );
}

export function ServiceTile({
  className,
  ...props
}: React.ComponentProps<typeof motion.div>) {
  return (
    <motion.div
      className={cn(
        "group bg-card border w-full rounded-[24px] px-4 data-[status='error']:border-destructive flex flex-col",
        className,
      )}
      {...props}
    />
  );
}

export function ServiceTileExpanded({
  className,
  expanded = false,
  ...props
}: React.ComponentProps<typeof motion.div> & { expanded?: boolean }) {
  return (
    <AnimatePresence>
      {expanded && (
        <motion.div
          initial={{
            height: 0,
          }}
          animate={{
            height: "auto",
          }}
          exit={{
            height: 0,
            transition: {
              bounce: 0,
              type: "spring",
            },
          }}
          transition={{
            bounce: 0,
            type: "spring",
          }}
          {...props}
          className={cn("overflow-hidden", className)}
        />
      )}
    </AnimatePresence>
  );
}

export function ServiceTileScope({
  className,
  ...props
}: React.ComponentProps<"span">) {
  return (
    <span
      {...props}
      className={cn(
        "text-sm text-muted-foreground after:content-['/'] after:ml-2 after:text-muted-foreground",
        className,
      )}
    />
  );
}

export function ServiceTileIcon() {
  return (
    <>
      <Wifi className="group-data-[status='ok']:inline-block hidden text-teal-500" />
      <WifiHigh className="group-data-[status='warning']:inline-block hidden text-amber-400" />
      <WifiOff className="group-data-[status='error']:inline-block hidden text-rose-400" />
    </>
  );
}

export function ServiceTileTitle({
  className,
  ...props
}: React.ComponentProps<"span">) {
  return (
    <span
      {...props}
      className={cn(
        "inline-flex flex-1 items-baseline gap-2 font-sans [&_svg]:size-4 [&_svg]:animate-pulse [&_svg]:translate-y-0.5",
        className,
      )}
    />
  );
}

export function ServiceTileSummary({
  className,
  ...props
}: React.ComponentProps<"div">) {
  return <div className={cn("h-5 w-10 inline", className)} {...props} />;
}

type ServiceTilePingProps = React.ComponentProps<typeof Badge> & {
  title: string;
  description: string;
};

export function ServiceTilePing({
  className,
  title,
  description,
  ...props
}: ServiceTilePingProps) {
  return (
    <StatusHoverCard title={title} description={description}>
      <Badge
        variant="secondary"
        className={cn("capitalize", className)}
        {...props}
      />
    </StatusHoverCard>
  );
}

export function ServiceTilePingDot({
  className,
  ...props
}: React.ComponentProps<"div">) {
  return (
    <div
      {...props}
      className={cn(
        "size-2 rounded-full group-data-[status='ok']:bg-teal-500 group-data-[status='warning']:bg-amber-400 group-data-[status='error']:bg-rose-400",
        className,
      )}
    />
  );
}
