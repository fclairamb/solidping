import { cn } from "@/lib/utils";
import { statusStyle } from "@/lib/status-style";

interface StatusDotProps {
  status?: string | null;
  /** When false, the check is paused → grey dot, overriding status colour. */
  enabled?: boolean;
  /** Size/extra classes; default size is h-2.5 w-2.5. */
  className?: string;
  /** Tooltip + accessible label (e.g. the localized "Disabled"). */
  title?: string;
}

// Shared status dot for the operator UI. Non-disabled colours come from the
// single-source-of-truth statusStyle(), so the dot always matches the
// StatusBadge rendered beside it. A disabled check (enabled === false) renders
// a neutral grey dot — the paused state takes precedence over the last/live
// status colour. Grey uses the bg-muted-foreground theme token (correct in both
// light and dark mode); opacity-70 makes it read as "off" without a new colour.
export function StatusDot({ status, enabled, className, title }: StatusDotProps) {
  const disabled = enabled === false;
  const color = disabled ? "bg-muted-foreground" : statusStyle(status).color;
  return (
    <span
      data-testid="check-status-dot"
      data-disabled={disabled ? "true" : "false"}
      data-status={status ?? "unknown"}
      title={title}
      aria-label={title}
      className={cn(
        "inline-block h-2.5 w-2.5 shrink-0 rounded-full",
        color,
        disabled && "opacity-70",
        className,
      )}
    />
  );
}
