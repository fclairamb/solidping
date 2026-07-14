import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { X } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { isValidEmail, parseEmailList } from "@/lib/email";
import { cn } from "@/lib/utils";

export interface RecipientsInputProps {
  value: string[];
  onChange: (next: string[]) => void;
  placeholder?: string;
  id?: string;
  "data-testid"?: string;
}

// RecipientsInput is a chip/tag input for a list of free-form addresses
// (currently used for email recipients). Each entry renders as a removable
// Badge chip — destructive-red when it fails isValidEmail, so invalid entries
// are visibly flagged without being silently dropped. A free-text input sits
// alongside the chips: typing a separator (space/comma/semicolon), pressing
// Enter, pasting a separator-delimited blob, or blurring the field all commit
// the current token(s) to chips via parseEmailList. Backspace on an empty
// input pops the last chip. Modeled on the chip rendering in
// check-multi-picker.tsx; dismiss uses the lucide X icon (not Trash2 — an
// unsaved chip is not a resource deletion).
export function RecipientsInput({
  value,
  onChange,
  placeholder,
  id,
  "data-testid": testid,
}: RecipientsInputProps) {
  const { t } = useTranslation("integrations");
  const [draft, setDraft] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  function commitFrom(raw: string) {
    const tokens = parseEmailList(raw);
    if (tokens.length === 0) return;
    const additions = tokens.filter((tok) => !value.includes(tok));
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
      className="flex min-h-9 flex-wrap items-center gap-1.5 rounded-md border border-input bg-background px-2 py-1.5 focus-within:ring-1 focus-within:ring-ring"
      data-testid={testid}
      onClick={() => inputRef.current?.focus()}
    >
      {value.map((email, i) => {
        const valid = isValidEmail(email);
        return (
          <Badge
            key={`${email}-${i}`}
            variant={valid ? "secondary" : "destructive"}
            className="gap-1 py-1 pr-1"
            title={
              valid
                ? undefined
                : t("form.recipientInvalid", "Not a valid email address")
            }
            data-testid={testid ? `${testid}-chip-${i}` : undefined}
          >
            <span className="max-w-[16rem] truncate">{email}</span>
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation();
                removeChip(i);
              }}
              aria-label={t("form.removeRecipient", "Remove {{email}}", {
                email,
              })}
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
