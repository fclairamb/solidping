import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

const alertVariants = cva(
  "relative w-full rounded-lg border px-4 py-3 text-sm grid has-[>svg]:grid-cols-[calc(var(--spacing)*5)_1fr] grid-cols-[0_1fr] has-[>svg]:gap-x-3 gap-y-0.5 items-start [&>svg]:size-5 [&>svg]:text-current",
  {
    variants: {
      variant: {
        // Body text stays neutral for readability; the icon carries the hue so
        // the banner's kind reads at a glance. Same rule as the typed toasts.
        default:
          "border-primary/40 bg-primary/5 text-foreground [&>svg]:text-primary",
        destructive:
          "border-status-error/40 bg-status-error/10 text-status-error-foreground [&>svg]:text-current *:data-[slot=alert-description]:text-status-error-foreground/90",
        success:
          "border-status-ok/40 bg-status-ok/10 text-status-ok-foreground [&>svg]:text-current *:data-[slot=alert-description]:text-status-ok-foreground/90",
        warning:
          "border-status-warning/40 bg-status-warning/10 text-status-warning-foreground [&>svg]:text-current *:data-[slot=alert-description]:text-status-warning-foreground/90",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  }
);

function Alert({
  className,
  variant,
  ...props
}: React.ComponentProps<"div"> & VariantProps<typeof alertVariants>) {
  return (
    <div
      data-slot="alert"
      role="alert"
      className={cn(alertVariants({ variant }), className)}
      {...props}
    />
  );
}

function AlertTitle({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="alert-title"
      className={cn(
        "col-start-2 line-clamp-1 min-h-4 font-medium tracking-tight",
        className
      )}
      {...props}
    />
  );
}

function AlertDescription({
  className,
  ...props
}: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="alert-description"
      className={cn(
        "text-muted-foreground col-start-2 grid justify-items-start gap-1 text-sm [&_p]:leading-relaxed",
        className
      )}
      {...props}
    />
  );
}

export { Alert, AlertTitle, AlertDescription };
