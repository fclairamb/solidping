import { useRef, useState } from "react";
import { X } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

export interface TokenChipsInputProps {
  value: string[];
  onChange: (next: string[]) => void;
  /** A token renders as a destructive-red chip when this returns false. */
  validate: (token: string) => boolean;
  /**
   * Applied to every token at commit time (typing a separator, Enter, blur,
   * or paste) — e.g. trim + uppercase for HTTP status patterns. When absent,
   * tokens are kept exactly as typed (trimmed), which is what email
   * recipients need: the local part of an address is case-sensitive.
   */
  normalize?: (token: string) => string;
  placeholder?: string;
  id?: string;
  "data-testid"?: string;
  /** Title/tooltip shown on a chip that fails `validate`. */
  invalidTitle?: string;
  /** aria-label for a chip's remove button; defaults to `Remove {token}`. */
  getRemoveLabel?: (token: string) => string;
}

// TokenChipsInput is a generic chip/tag input for a free-form list of
// validated string tokens. Originally built as RecipientsInput for the email
// integration's multiple recipients, then generalized (spec
// 2026-07-21-02-http-expected-status-codes-chip-input) so the HTTP check
// form's expected-status patterns (e.g. "200", "4XX") share the same
// component instead of a second bespoke implementation. Each entry renders as
// a removable Badge chip — destructive-red when it fails the caller's
// `validate`, so invalid entries are visibly flagged without being silently
// dropped. A free-text input sits alongside the chips: typing a separator
// (space/comma/semicolon), pressing Enter, pasting a separator-delimited
// blob, or blurring the field all commit the current token(s) to chips (each
// token running through `normalize` first, when given, then de-duplicated
// against what's already there). Backspace on an empty input pops the last
// chip. Dismiss uses the lucide X icon (not Trash2 — an unsaved chip is not a
// resource deletion).
export function TokenChipsInput({
  value,
  onChange,
  validate,
  normalize,
  placeholder,
  id,
  "data-testid": testid,
  invalidTitle,
  getRemoveLabel,
}: TokenChipsInputProps) {
  const [draft, setDraft] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  // splitTokens breaks a raw chunk of text on any run of whitespace, commas,
  // or semicolons — covering space/comma/semicolon/newline and any mix
  // thereof (e.g. pasting "a@x.com, b@x.com" or "200, 4xx"). Entries are
  // trimmed, empty strings dropped, normalized (if `normalize` is given), and
  // de-duplicated on the normalized value while preserving first-seen order.
  function splitTokens(raw: string): string[] {
    const seen = new Set<string>();
    const out: string[] = [];
    for (const token of raw.split(/[\s,;]+/)) {
      const trimmed = token.trim();
      if (!trimmed) continue;
      const normalized = normalize ? normalize(trimmed) : trimmed;
      if (!normalized || seen.has(normalized)) continue;
      seen.add(normalized);
      out.push(normalized);
    }
    return out;
  }

  function commitFrom(raw: string) {
    const tokens = splitTokens(raw);
    if (tokens.length === 0) return;
    const existing = new Set(value.map((v) => (normalize ? normalize(v) : v)));
    const additions = tokens.filter((tok) => {
      if (existing.has(tok)) return false;
      existing.add(tok);
      return true;
    });
    if (additions.length > 0) onChange([...value, ...additions]);
  }

  function handleChange(e: React.ChangeEvent<HTMLInputElement>) {
    const val = e.target.value;
    const lastChar = val.length > 0 ? val[val.length - 1] : "";
    if (lastChar && /[\s,;]/.test(lastChar)) {
      commitFrom(val);
      setDraft("");
      return;
    }
    setDraft(val);
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (e.key === "Enter") {
      e.preventDefault();
      if (draft.trim()) {
        commitFrom(draft);
        setDraft("");
      }
      return;
    }
    if (e.key === "Backspace" && draft === "" && value.length > 0) {
      onChange(value.slice(0, -1));
    }
  }

  function handleBlur() {
    if (draft.trim()) commitFrom(draft);
    setDraft("");
  }

  function handlePaste(e: React.ClipboardEvent<HTMLInputElement>) {
    const pasted = e.clipboardData.getData("text");
    if (!/[\s,;]/.test(pasted)) return; // single token — let default paste land in the draft
    e.preventDefault();
    commitFrom(draft + pasted);
    setDraft("");
  }

  function removeChip(index: number) {
    onChange(value.filter((_, i) => i !== index));
  }

  return (
    <div
      className="flex min-h-9 flex-wrap items-center gap-1.5 rounded-md border border-input bg-control px-2 py-1.5 focus-within:ring-1 focus-within:ring-ring"
      data-testid={testid}
      onClick={() => inputRef.current?.focus()}
    >
      {value.map((token, i) => {
        const valid = validate(token);
        return (
          <Badge
            key={`${token}-${i}`}
            variant={valid ? "secondary" : "destructive"}
            className="gap-1 py-1 pr-1"
            title={valid ? undefined : invalidTitle}
            data-testid={testid ? `${testid}-chip-${i}` : undefined}
          >
            <span className="max-w-[16rem] truncate">{token}</span>
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation();
                removeChip(i);
              }}
              aria-label={
                getRemoveLabel ? getRemoveLabel(token) : `Remove ${token}`
              }
              className={cn(
                // Visually a small 12px X, but padded to a ~24px hit target
                // via negative margin so it stays touch-friendly on mobile
                // without inflating the chip's visual size.
                "-m-1 inline-flex h-6 w-6 items-center justify-center rounded-sm p-1.5 hover:bg-black/10 dark:hover:bg-white/10",
              )}
              data-testid={testid ? `${testid}-chip-remove-${i}` : undefined}
            >
              <X className="h-3 w-3" />
            </button>
          </Badge>
        );
      })}
      <input
        id={id}
        ref={inputRef}
        value={draft}
        onChange={handleChange}
        onKeyDown={handleKeyDown}
        onBlur={handleBlur}
        onPaste={handlePaste}
        placeholder={value.length === 0 ? placeholder : ""}
        className="h-7 min-w-[10rem] flex-1 border-0 bg-transparent p-1 text-sm outline-none placeholder:text-muted-foreground"
        data-testid={testid ? `${testid}-input` : undefined}
      />
    </div>
  );
}
