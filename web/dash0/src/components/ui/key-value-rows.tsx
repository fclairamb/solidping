import { Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

export interface KeyValueRow {
  key: string;
  value: string;
}

export interface KeyValueRowsProps {
  rows: KeyValueRow[];
  onChange: (rows: KeyValueRow[]) => void;
  /** Label of the "add a row" button. */
  addLabel: string;
  keyPlaceholder?: string;
  valuePlaceholder?: string;
  /** Renders the value inputs as password fields (secret editors). */
  secretValues?: boolean;
  /** Accessible label for a row's remove button; receives the row's key. */
  removeLabel?: (key: string) => string;
  /**
   * `data-testid` prefix. A row gets `${testIdPrefix}-key-${i}`,
   * `${testIdPrefix}-value-${i}` and `${testIdPrefix}-remove-${i}`; the add
   * button gets `${testIdPrefix}-add`.
   */
  testIdPrefix: string;
  className?: string;
}

/**
 * KeyValueRows is the shared key/value row editor used for request headers,
 * secret headers, gRPC metadata and similar `Record<string, string>` config
 * keys.
 *
 * Two behaviours are load-bearing and must not be "simplified" away:
 *
 * - **Adding a blank row does not notify.** The button appends a row locally
 *   through `onChange`, but callers that keep a dirty flag only set it when a
 *   row's text actually changes; a stray click on "add" followed by a save must
 *   not clear stored values.
 * - **Rows are keyed by index.** Keying by `row.key` would remount the input on
 *   every keystroke and lose focus while typing a header name.
 *
 * Mobile: the key and value inputs stack below `sm` so a 375px viewport never
 * needs horizontal scrolling; the remove button stays on the key row.
 */
export function KeyValueRows({
  rows,
  onChange,
  addLabel,
  keyPlaceholder,
  valuePlaceholder,
  secretValues,
  removeLabel,
  testIdPrefix,
  className,
}: KeyValueRowsProps) {
  function patch(idx: number, patchRow: Partial<KeyValueRow>) {
    const updated = [...rows];
    updated[idx] = { ...updated[idx], ...patchRow };
    onChange(updated);
  }

  return (
    <div className={cn("space-y-2", className)}>
      {rows.map((row, idx) => (
        <div
          key={idx}
          className="flex flex-col gap-2 sm:flex-row sm:items-center"
        >
          <Input
            type="text"
            placeholder={keyPlaceholder}
            value={row.key}
            onChange={(e) => patch(idx, { key: e.target.value })}
            className="flex-1 min-w-0"
            data-testid={`${testIdPrefix}-key-${idx}`}
          />
          <Input
            type={secretValues ? "password" : "text"}
            placeholder={valuePlaceholder}
            value={row.value}
            onChange={(e) => patch(idx, { value: e.target.value })}
            className="flex-1 min-w-0"
            data-testid={`${testIdPrefix}-value-${idx}`}
          />
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="text-destructive shrink-0 self-end sm:self-auto"
            aria-label={removeLabel?.(row.key)}
            onClick={() => onChange(rows.filter((_, i) => i !== idx))}
            data-testid={`${testIdPrefix}-remove-${idx}`}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      ))}
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => onChange([...rows, { key: "", value: "" }])}
        data-testid={`${testIdPrefix}-add`}
      >
        <Plus className="h-4 w-4 mr-1" />
        {addLabel}
      </Button>
    </div>
  );
}
