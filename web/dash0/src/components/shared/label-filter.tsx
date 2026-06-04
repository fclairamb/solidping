import { Command } from "cmdk";
import { ChevronLeft, Tags, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import { useLabelSuggestions } from "@/api/hooks";
import {
  KEY_ERROR,
  KEY_REGEX,
  SUGGESTION_DEBOUNCE_MS,
  SUGGESTION_LIMIT,
  useDebounced,
  VALUE_MAX,
} from "@/components/shared/label-shared";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";

export type LabelFilterProps = {
  org: string;
  value: Record<string, string>;
  onChange: (next: Record<string, string>) => void;
};

// LabelFilter is the checks-list faceted label picker: applied filters render
// as removable chips and a single compact "+ Label" trigger opens one popover
// with a guided two-step cmdk list (pick a key, then pick/type a value).
// Selecting a value applies the filter immediately — there is no Add button.
// The URL contract (?labels=key:value,…) is owned by the caller via onChange.
export function LabelFilter({ org, value, onChange }: LabelFilterProps) {
  const [open, setOpen] = useState(false);
  const [step, setStep] = useState<"key" | "value">("key");
  const [activeKey, setActiveKey] = useState("");
  const [keyDraft, setKeyDraft] = useState("");
  const [valueDraft, setValueDraft] = useState("");

  const keyInputRef = useRef<HTMLInputElement>(null);
  const valueInputRef = useRef<HTMLInputElement>(null);

  // Reset to a clean step-1 state. Called when the popover closes (in the
  // onOpenChange handler, not an effect) and after applying a filter.
  const resetPicker = () => {
    setStep("key");
    setActiveKey("");
    setKeyDraft("");
    setValueDraft("");
  };

  // Focus the relevant input when the step changes (popover suppresses
  // auto-focus so cmdk keyboard nav keeps working).
  useEffect(() => {
    if (!open) return;
    const id = window.setTimeout(() => {
      if (step === "key") keyInputRef.current?.focus();
      else valueInputRef.current?.focus();
    }, 0);
    return () => window.clearTimeout(id);
  }, [open, step]);

  const remove = (key: string) => {
    const next = { ...value };
    delete next[key];
    onChange(next);
  };

  const applyValue = (key: string, val: string) => {
    onChange({ ...value, [key]: val });
    // Stay open and reset to step 1 so several filters can be stacked quickly.
    resetPicker();
  };

  const chooseKey = (key: string) => {
    if (value[key]) {
      // Already filtering by this key — don't advance; the inline hint explains.
      return;
    }
    setActiveKey(key);
    setStep("value");
    setValueDraft("");
  };

  const entries = Object.entries(value).sort(([a], [b]) => a.localeCompare(b));

  return (
    <div className="flex flex-wrap items-center gap-2">
      {entries.length > 0 && (
        <div className="flex flex-wrap gap-2" data-testid="label-chips">
          {entries.map(([k, v]) => (
            <Badge key={k} variant="secondary" className="gap-1 pr-1">
              <span>
                {k}: {v}
              </span>
              <button
                type="button"
                aria-label={`Remove ${k}`}
                onClick={() => remove(k)}
                className="ml-1 rounded-sm p-0.5 hover:bg-foreground/10"
                data-testid={`label-chip-remove-${k}`}
              >
                <X className="h-3 w-3" />
              </button>
            </Badge>
          ))}
        </div>
      )}

      <Popover
        open={open}
        onOpenChange={(next) => {
          setOpen(next);
          if (!next) resetPicker();
        }}
      >
        <PopoverTrigger asChild>
          <Button
            type="button"
            variant="outline"
            size="sm"
            data-testid="label-filter-trigger"
          >
            <Tags className="h-4 w-4" />
            Label
          </Button>
        </PopoverTrigger>
        <PopoverContent
          align="start"
          className="w-64 p-0"
          onOpenAutoFocus={(e) => e.preventDefault()}
        >
          {step === "key" ? (
            <KeyStep
              org={org}
              value={value}
              keyDraft={keyDraft}
              onKeyDraftChange={setKeyDraft}
              onChooseKey={chooseKey}
              inputRef={keyInputRef}
            />
          ) : (
            <ValueStep
              org={org}
              activeKey={activeKey}
              valueDraft={valueDraft}
              onValueDraftChange={setValueDraft}
              onApply={(val) => applyValue(activeKey, val)}
              onBack={() => {
                setStep("key");
                setActiveKey("");
              }}
              inputRef={valueInputRef}
            />
          )}
        </PopoverContent>
      </Popover>
    </div>
  );
}

function KeyStep({
  org,
  value,
  keyDraft,
  onKeyDraftChange,
  onChooseKey,
  inputRef,
}: {
  org: string;
  value: Record<string, string>;
  keyDraft: string;
  onKeyDraftChange: (v: string) => void;
  onChooseKey: (key: string) => void;
  inputRef: React.RefObject<HTMLInputElement | null>;
}) {
  const debounced = useDebounced(keyDraft, SUGGESTION_DEBOUNCE_MS);
  const { data: suggestions = [] } = useLabelSuggestions(org, {
    q: debounced,
    limit: SUGGESTION_LIMIT,
  });

  const trimmed = keyDraft.trim();
  const keyValid = KEY_REGEX.test(trimmed);
  const showKeyError = trimmed !== "" && !keyValid;
  const matchesExisting = suggestions.some((s) => s.value === trimmed);
  const showUseTyped = keyValid && !matchesExisting;
  const alreadyFiltering = trimmed !== "" && !!value[trimmed];

  return (
    <Command shouldFilter={false}>
      <Command.Input
        ref={inputRef}
        value={keyDraft}
        onValueChange={onKeyDraftChange}
        placeholder="Filter by label…"
        className="w-full border-0 border-b border-border bg-transparent px-3 py-2.5 text-sm outline-none placeholder:text-muted-foreground"
        data-testid="label-filter-key-input"
      />
      <Command.List className="max-h-56 overflow-y-auto p-1">
        {alreadyFiltering && (
          <p
            className="px-2 py-1.5 text-xs text-muted-foreground"
            data-testid="label-filter-already"
          >
            Already filtering by “{trimmed}”.
          </p>
        )}
        {showUseTyped && !alreadyFiltering && (
          <Command.Item
            key="__use_typed_key__"
            value={`use-${trimmed}`}
            onSelect={() => onChooseKey(trimmed)}
            className="flex cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 text-sm aria-selected:bg-accent aria-selected:text-accent-foreground"
            data-testid="label-filter-key-use-typed"
          >
            Use “{trimmed}”
          </Command.Item>
        )}
        {suggestions.length === 0 && !showUseTyped && !showKeyError && (
          <Command.Empty className="px-2 py-3 text-center text-xs text-muted-foreground">
            No labels
          </Command.Empty>
        )}
        {suggestions.map((s) => {
          const disabled = !!value[s.value];
          return (
            <Command.Item
              key={s.value}
              value={s.value}
              onSelect={() => onChooseKey(s.value)}
              disabled={disabled}
              className="flex cursor-pointer items-center justify-between rounded-sm px-2 py-1.5 text-sm aria-selected:bg-accent aria-selected:text-accent-foreground data-[disabled=true]:cursor-not-allowed data-[disabled=true]:opacity-50"
            >
              <span>{s.value}</span>
              <span className="text-xs text-muted-foreground">{s.count}</span>
            </Command.Item>
          );
        })}
      </Command.List>
      {showKeyError && (
        <p
          className="border-t border-border px-3 py-2 text-xs text-destructive"
          data-testid="label-filter-key-error"
        >
          {KEY_ERROR}
        </p>
      )}
    </Command>
  );
}

function ValueStep({
  org,
  activeKey,
  valueDraft,
  onValueDraftChange,
  onApply,
  onBack,
  inputRef,
}: {
  org: string;
  activeKey: string;
  valueDraft: string;
  onValueDraftChange: (v: string) => void;
  onApply: (val: string) => void;
  onBack: () => void;
  inputRef: React.RefObject<HTMLInputElement | null>;
}) {
  const debounced = useDebounced(valueDraft, SUGGESTION_DEBOUNCE_MS);
  const { data: suggestions = [] } = useLabelSuggestions(org, {
    key: activeKey,
    q: debounced,
    limit: SUGGESTION_LIMIT,
  });

  const trimmed = valueDraft.trim();
  const valueValid = trimmed.length > 0 && trimmed.length <= VALUE_MAX;
  const matchesExisting = suggestions.some((s) => s.value === trimmed);
  const showUseTyped = valueValid && !matchesExisting;
  const tooLong = trimmed.length > VALUE_MAX;

  return (
    <Command shouldFilter={false}>
      <div className="flex items-center gap-1 border-b border-border px-1.5 py-1.5">
        <button
          type="button"
          onClick={onBack}
          aria-label="Back to keys"
          className="inline-flex h-7 items-center gap-0.5 rounded-sm px-1 text-sm font-medium hover:bg-accent hover:text-accent-foreground"
          data-testid="label-filter-back"
        >
          <ChevronLeft className="h-4 w-4" />
          <span>{activeKey}</span>
        </button>
        <span className="text-muted-foreground">:</span>
        <Command.Input
          ref={inputRef}
          value={valueDraft}
          onValueChange={onValueDraftChange}
          placeholder="value…"
          className="flex-1 border-0 bg-transparent px-1 py-1 text-sm outline-none placeholder:text-muted-foreground"
          data-testid="label-filter-value-input"
        />
      </div>
      <Command.List className="max-h-56 overflow-y-auto p-1">
        {showUseTyped && (
          <Command.Item
            key="__use_typed_value__"
            value={`use-${trimmed}`}
            onSelect={() => onApply(trimmed)}
            className="flex cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 text-sm aria-selected:bg-accent aria-selected:text-accent-foreground"
            data-testid="label-filter-value-use-typed"
          >
            Use “{trimmed}”
          </Command.Item>
        )}
        {suggestions.length === 0 && !showUseTyped && (
          <Command.Empty className="px-2 py-3 text-center text-xs text-muted-foreground">
            No values
          </Command.Empty>
        )}
        {suggestions.map((s) => (
          <Command.Item
            key={s.value}
            value={s.value}
            onSelect={() => onApply(s.value)}
            className="flex cursor-pointer items-center justify-between rounded-sm px-2 py-1.5 text-sm aria-selected:bg-accent aria-selected:text-accent-foreground"
          >
            <span>{s.value}</span>
            <span className="text-xs text-muted-foreground">{s.count}</span>
          </Command.Item>
        ))}
      </Command.List>
      {tooLong && (
        <p className="border-t border-border px-3 py-2 text-xs text-destructive">
          Value must be at most {VALUE_MAX} characters.
        </p>
      )}
    </Command>
  );
}
