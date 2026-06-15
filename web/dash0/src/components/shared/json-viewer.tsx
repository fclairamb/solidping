import { cn } from "@/lib/utils";

// JsonViewer renders a read-only, pretty-printed JSON block in a monospace,
// scrollable panel. Used by the admin Jobs detail pages for config/output.
export function JsonViewer({
  value,
  emptyLabel = "—",
  className,
}: {
  value: unknown;
  emptyLabel?: string;
  className?: string;
}) {
  const isEmpty =
    value === null ||
    value === undefined ||
    (typeof value === "object" && Object.keys(value as object).length === 0);

  if (isEmpty) {
    return <p className="text-sm text-muted-foreground">{emptyLabel}</p>;
  }

  let formatted: string;
  try {
    formatted = JSON.stringify(value, null, 2);
  } catch {
    formatted = String(value);
  }

  return (
    <pre
      className={cn(
        "max-h-96 overflow-auto rounded-md border bg-muted/50 p-3 text-xs leading-relaxed font-mono text-foreground",
        className,
      )}
    >
      <code>{formatted}</code>
    </pre>
  );
}
