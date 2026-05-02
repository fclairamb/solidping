import { Command } from "cmdk";
import { X } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import { useLabelSuggestions } from "@/api/hooks";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";

const KEY_REGEX = /^[a-z][a-z0-9-]{3,50}$/;
const VALUE_MAX = 200;
const SUGGESTION_DEBOUNCE_MS = 200;
const SUGGESTION_LIMIT = 25;

export type LabelInputProps = {
  org: string;
  value: Record<string, string>;
  onChange: (next: Record<string, string>) => void;
  disabled?: boolean;
  placeholder?: { key?: string; value?: string };
};

export function LabelInput({ org, value, onChange, disabled, placeholder }: LabelInputProps) {
  const [keyDraft, setKeyDraft] = useState("");
  const [valueDraft, setValueDraft] = useState("");
  const valueInputRef = useRef<HTMLInputElement>(null);

  const debouncedKey = useDebounced(keyDraft, SUGGESTION_DEBOUNCE_MS);
  const debouncedValue = useDebounced(valueDraft, SUGGESTION_DEBOUNCE_MS);

  const trimmedKey = keyDraft.trim();
  const trimmedValue = valueDraft.trim();
  const keyValid = KEY_REGEX.test(trimmedKey);
  const valueValid = trimmedValue.length > 0 && trimmedValue.length <= VALUE_MAX;
  const duplicate = !!value[trimmedKey];
  const canAdd = !disabled && keyValid && valueValid && !duplicate;

  const commit = () => {
    if (!canAdd) return;
    onChange({ ...value, [trimmedKey]: trimmedValue });
    setKeyDraft("");
    setValueDraft("");
  };

  const remove = (key: string) => {
    if (disabled) return;
    const next = { ...value };
    delete next[key];
    onChange(next);
  };

  const entries = Object.entries(value).sort(([a], [b]) => a.localeCompare(b));

  return (
    <div className="space-y-2">
      {entries.length > 0 && (
        <div className="flex flex-wrap gap-2" data-testid="label-chips">
          {entries.map(([k, v]) => (
            <Badge key={k} variant="secondary" className="gap-1 pr-1">
              <span>{k}: {v}</span>
              {!disabled && (
                <button
                  type="button"
                  aria-label={`Remove ${k}`}
                  onClick={() => remove(k)}
                  className="ml-1 rounded-sm p-0.5 hover:bg-foreground/10"
                  data-testid={`label-chip-remove-${k}`}
                >
                  <X className="h-3 w-3" />
                </button>
              )}
            </Badge>
          ))}
        </div>
      )}

      <div className="flex flex-wrap items-start gap-2">
        <SuggestionCombobox
          org={org}
          mode="key"
          inputValue={keyDraft}
          onInputChange={setKeyDraft}
          onCommit={(v) => {
            setKeyDraft(v);
            valueInputRef.current?.focus();
          }}
          query={debouncedKey}
          disabled={disabled}
          placeholder={placeholder?.key ?? "key"}
        />
        <span className="px-1 pt-2 text-muted-foreground">:</span>
        <SuggestionCombobox
          org={org}
          mode="value"
          forKey={keyValid ? trimmedKey : undefined}
          inputRef={valueInputRef}
          inputValue={valueDraft}
          onInputChange={setValueDraft}
          onCommit={(v) => {
            setValueDraft(v);
          }}
          onEnterCommit={commit}
          query={debouncedValue}
          disabled={disabled}
          placeholder={placeholder?.value ?? "value"}
        />
        <Button type="button" onClick={commit} disabled={!canAdd} size="sm">
          Add
        </Button>
      </div>

      {trimmedKey !== "" && !keyValid && (
        <p className="text-xs text-destructive" data-testid="label-key-error">
          Use 4–51 lowercase letters, digits, or hyphens, starting with a letter.
        </p>
      )}
      {keyValid && duplicate && (
        <p className="text-xs text-destructive" data-testid="label-key-duplicate">
          This key is already set — edit the existing label.
        </p>
      )}
      {trimmedValue.length > VALUE_MAX && (
        <p className="text-xs text-destructive">
          Value must be at most {VALUE_MAX} characters.
        </p>
      )}
    </div>
  );
}

type ComboboxProps = {
  org: string;
  mode: "key" | "value";
  forKey?: string;
  inputValue: string;
  onInputChange: (v: string) => void;
  onCommit: (v: string) => void;
  onEnterCommit?: () => void;
  query: string;
  disabled?: boolean;
  placeholder?: string;
  inputRef?: React.RefObject<HTMLInputElement | null>;
};

function SuggestionCombobox({
  org,
  mode,
  forKey,
  inputValue,
  onInputChange,
  onCommit,
  onEnterCommit,
  query,
  disabled,
  placeholder,
  inputRef,
}: ComboboxProps) {
  const [open, setOpen] = useState(false);

  const enabled =
    !disabled && open && (mode === "key" ? true : !!forKey);

  const { data: suggestions = [] } = useLabelSuggestions(org, {
    key: mode === "value" ? forKey : undefined,
    q: query,
    limit: SUGGESTION_LIMIT,
    enabled,
  });

  const trimmed = inputValue.trim();
  const matchesExisting = suggestions.some((s) => s.value === trimmed);
  const showUseTyped = trimmed !== "" && !matchesExisting;

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <input
          ref={inputRef}
          type="text"
          value={inputValue}
          disabled={disabled}
          onFocus={() => setOpen(true)}
          onChange={(e) => {
            onInputChange(e.target.value);
            if (!open) setOpen(true);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter" && onEnterCommit) {
              e.preventDefault();
              onEnterCommit();
            }
          }}
          placeholder={placeholder}
          className={cn(
            "h-9 w-40 rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm",
            "focus:outline-none focus:ring-2 focus:ring-ring",
            "disabled:cursor-not-allowed disabled:opacity-50"
          )}
          data-testid={`label-${mode}-input`}
        />
      </PopoverTrigger>
      <PopoverContent
        align="start"
        className="w-64 p-0"
        onOpenAutoFocus={(e) => e.preventDefault()}
      >
        <Command shouldFilter={false}>
          <Command.List className="max-h-56 overflow-y-auto p-1">
            {showUseTyped && (
              <Command.Item
                key="__use_typed__"
                value={`use-${trimmed}`}
                onSelect={() => {
                  onCommit(trimmed);
                  setOpen(false);
                }}
                className="flex cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 text-sm aria-selected:bg-accent aria-selected:text-accent-foreground"
                data-testid={`label-${mode}-use-typed`}
              >
                Use &ldquo;{trimmed}&rdquo;
              </Command.Item>
            )}
            {suggestions.length === 0 && !showUseTyped && (
              <Command.Empty className="px-2 py-3 text-center text-xs text-muted-foreground">
                No matches
              </Command.Empty>
            )}
            {suggestions.map((s) => (
              <Command.Item
                key={s.value}
                value={s.value}
                onSelect={() => {
                  onCommit(s.value);
                  setOpen(false);
                }}
                className="flex cursor-pointer items-center justify-between rounded-sm px-2 py-1.5 text-sm aria-selected:bg-accent aria-selected:text-accent-foreground"
              >
                <span>{s.value}</span>
                <span className="text-xs text-muted-foreground">{s.count}</span>
              </Command.Item>
            ))}
          </Command.List>
        </Command>
      </PopoverContent>
    </Popover>
  );
}

function useDebounced<T>(value: T, delayMs: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setDebounced(value), delayMs);
    return () => clearTimeout(id);
  }, [value, delayMs]);
  return debounced;
}
