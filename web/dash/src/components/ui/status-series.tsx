import { cn } from "@/lib/utils";

export function StatusSeries({
  className,
  ...props
}: React.ComponentProps<"div">) {
  return <div className={cn("flex h-full gap-1", className)} {...props} />;
}

export function StatusSeriesItem({
  className,
  ...props
}: React.ComponentProps<"div">) {
  return (
    <div
      className={cn("group/status-item h-full flex-1", className)}
      {...props}
    />
  );
}

export function StatusSeriesBar({
  className,
  ...props
}: React.ComponentProps<"div">) {
  return (
    <div
      className={cn(
        "w-full h-full rounded-full",
        "group-data-[status='ok']/status-item:bg-teal-500",
        "group-data-[status='error']/status-item:bg-rose-400",
        "group-data-[status='warning']/status-item:bg-amber-400",
        className,
      )}
      {...props}
    />
  );
}
