import { cn } from "@/lib/utils";

export function ServiceSummary({
  className,
  ...props
}: React.ComponentProps<"div">) {
  return (
    <div
      className={cn(
        "flex flex-col gap-2 border rounded-md py-2 px-4",
        "group-data-[status='ok']:border-teal-500/50 group-data-[status='ok']:bg-teal-400/5",
        "group-data-[status='warning']:border-amber-400/50 group-data-[status='warning']:bg-amber-400/5",
        "group-data-[status='error']:border-rose-400/50 group-data-[status='error']:bg-rose-400/5",
        className,
      )}
      {...props}
    />
  );
}

export function ServiceSummaryTitle({
  className,
  ...props
}: React.ComponentProps<"span">) {
  return (
    <span
      className={cn(
        "font-semibold text-sm",
        "group-data-[status='ok']:text-teal-500",
        "group-data-[status='warning']:text-amber-600",
        "group-data-[status='error']:text-rose-400",
        className,
      )}
      {...props}
    />
  );
}

export function ServiceSummaryDescription({
  className,
  ...props
}: React.ComponentProps<"p">) {
  return (
    <p
      className={cn(
        "text-xs",
        "group-data-[status='ok']:text-teal-500",
        "group-data-[status='warning']:text-amber-600",
        "group-data-[status='error']:text-rose-400",
        className,
      )}
      {...props}
    />
  );
}
