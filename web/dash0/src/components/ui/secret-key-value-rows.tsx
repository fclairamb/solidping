import { Label } from "@/components/ui/label";
import {
  KeyValueRows,
  type KeyValueRow,
} from "@/components/ui/key-value-rows";

/**
 * secretRowsBecameDirty decides whether a row-list change must DIRTY a secret
 * section — the single rule the whole "save wipes the secret" bug class turns
 * on (specs 2026-05-18-07, 2026-08-28-12, 2026-09-11-05).
 *
 * Secret values never come back on a read, so an untouched editor is empty and
 * MUST NOT be serialized: omitting the key is what makes the server's
 * preserve-absent-secrets merge keep the stored value. The section therefore
 * starts clean and only the user's own edit may dirty it.
 *
 * Clicking "add" appends a blank row and is deliberately NOT an edit: a stray
 * click followed by a save must not clear the stored values. Anything else —
 * typing in a row, removing one — is. Row count is what separates the two:
 * `KeyValueRows` reports the whole next list, and only the add button makes it
 * longer.
 */
export function secretRowsBecameDirty(
  previous: KeyValueRow[],
  next: KeyValueRow[],
): boolean {
  return next.length <= previous.length;
}

export interface SecretKeyValueRowsProps {
  label: string;
  /** Help text under the label. */
  description?: string;
  rows: KeyValueRow[];
  /** Whether the section has already been touched in this editing session. */
  dirty: boolean;
  /**
   * Receives the next rows and the next dirty flag. Callers store both: the
   * flag is what their `toConfig` reads to decide whether to send the key.
   */
  onChange: (rows: KeyValueRow[], dirty: boolean) => void;
  /**
   * True when the server advertises a stored encrypted value for this config
   * key (`configPrivateKeys`). Renders the `••••` line while the section is
   * still untouched, so the operator can tell "nothing configured" from
   * "configured, just not shown".
   */
  stored?: boolean;
  /** Text of that line, e.g. "Encrypted — enter new values to replace". */
  storedLabel?: string;
  addLabel: string;
  keyPlaceholder?: string;
  valuePlaceholder?: string;
  removeLabel?: (key: string) => string;
  /** `data-testid` prefix; the stored line gets `${testIdPrefix}-encrypted`. */
  testIdPrefix: string;
}

/**
 * SecretKeyValueRows is `KeyValueRows` plus the two behaviours a WRITE-ONLY
 * `Record<string, string>` config key needs: the stored-value placeholder and
 * the dirty rule above.
 *
 * Use it for any encrypted map field (a JS check's `secrets`, an HTTP check's
 * secret headers). The plain `KeyValueRows` stays the right choice for a public
 * map, which round-trips on GET and needs no dirty flag at all.
 */
export function SecretKeyValueRows({
  label,
  description,
  rows,
  dirty,
  onChange,
  stored,
  storedLabel,
  addLabel,
  keyPlaceholder,
  valuePlaceholder,
  removeLabel,
  testIdPrefix,
}: SecretKeyValueRowsProps) {
  return (
    <div className="space-y-2">
      <div>
        <Label>{label}</Label>
        {description && (
          <p className="text-xs text-muted-foreground mt-0.5">{description}</p>
        )}
      </div>
      {stored && !dirty && (
        <p
          className="text-xs text-muted-foreground"
          data-testid={`${testIdPrefix}-encrypted`}
        >
          <span className="font-mono tracking-widest">••••</span>{" "}
          <span className="italic">{storedLabel}</span>
        </p>
      )}
      <KeyValueRows
        rows={rows}
        onChange={(next) =>
          onChange(next, dirty || secretRowsBecameDirty(rows, next))
        }
        addLabel={addLabel}
        keyPlaceholder={keyPlaceholder}
        valuePlaceholder={valuePlaceholder}
        secretValues
        removeLabel={removeLabel}
        testIdPrefix={testIdPrefix}
      />
    </div>
  );
}
