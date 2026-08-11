import { cn } from "@/lib/utils";

// StatTile is a compact metric tile used in the admin Jobs activity overview
// strip. `tone` tints the value to draw attention to error/warning counts.
export type StatTileTone = "default" | "info" | "warning" | "destructive" | "success";

const toneClass: Record<StatTileTone, string> = {
  default: "text-foreground",
  info: "text-blue-600 dark:text-blue-400",
  warning: "text-yellow-600 dark:text-yellow-400",
  destructive: "text-destructive",
  success: "text-green-600 dark:text-green-400",
};

export function StatTile({
  label,
  value,
  tone = "default",
  className,
}: {
  label: string;
  value: number | string;
  tone?: StatTileTone;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "rounded-lg border bg-card px-3 py-2 text-center",
        className,
      )}
    >
      <div
        className={cn(
          "text-2xl font-semibold tracking-tight tabular-nums",
          toneClass[tone],
        )}
      >
        {value}
      </div>
      <div className="mt-0.5 text-xs text-muted-foreground">{label}</div>
    </div>
  );
}
