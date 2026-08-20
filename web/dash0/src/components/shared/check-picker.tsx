import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Check, ChevronDown, X } from "lucide-react";

import { useCheck, useChecks, type Check as CheckType } from "@/api/hooks";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";

export interface CheckPickerProps {
  org: string;
  value?: string;
  excludeUids?: Set<string>;
  onChange: (uid: string | undefined, check?: CheckType) => void;
  placeholder?: string;
  disabled?: boolean;
  selectedLabel?: string;
  /** Optional data-testid for the trigger button (e.g. "badge-check-select"). */
  triggerTestId?: string;
}

const SEARCH_DEBOUNCE_MS = 150;
const SEARCH_LIMIT = 25;

export function CheckPicker({
  org,
  value,
  excludeUids,
  onChange,
  placeholder,
  disabled,
  selectedLabel,
  triggerTestId,
}: CheckPickerProps) {
  const { t } = useTranslation(["dependencies", "common"]);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [debouncedQuery, setDebouncedQuery] = useState("");
  const [activeIndex, setActiveIndex] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);

  // Self-resolved label for `value`, independent of the caller's optional
  // `selectedLabel` override. Set synchronously on select() so the trigger
  // never flashes the raw uid between picking an entity and the next render.
  const [pickedUid, setPickedUid] = useState<string | undefined>();
  const [pickedLabel, setPickedLabel] = useState<string | undefined>();

  useEffect(() => {
    const handle = setTimeout(() => setDebouncedQuery(query), SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(handle);
  }, [query]);

  const { data: matches = [] } = useChecks(org, {
    q: debouncedQuery || undefined,
    limit: SEARCH_LIMIT,
  });

  const filtered = useMemo(() => {
    if (!excludeUids) return matches;
    return matches.filter((c) => c.uid && !excludeUids.has(c.uid));
  }, [matches, excludeUids]);

  useEffect(() => {
    setActiveIndex(0);
  }, [debouncedQuery, filtered.length]);

  // A `value` set by anything other than this component's own select() (an
  // initial value on an edit form, restored state, …) needs its label
  // resolved: first from the already-fetched search results, and only if
  // that misses, by fetching the single check. `useCheck` shares its cache
  // key (["check", org, uid]) with every other consumer, so this is a no-op
  // request whenever the check was already fetched elsewhere.
  const needsResolve = !selectedLabel && !!value && value !== pickedUid;
  const fromList = needsResolve ? matches.find((c) => c.uid === value) : undefined;
  const {
    data: fetchedCheck,
    isError: fetchFailed,
  } = useCheck(org, needsResolve && !fromList ? value! : "", {});

  let triggerLabel: string;
  let isResolving = false;
  if (selectedLabel) {
    triggerLabel = selectedLabel;
  } else if (!value) {
    triggerLabel = placeholder ?? t("dependencies:pickCheck");
  } else if (value === pickedUid && pickedLabel) {
    triggerLabel = pickedLabel;
  } else if (fromList) {
    triggerLabel = fromList.name || fromList.slug || value;
  } else if (fetchedCheck) {
    triggerLabel = fetchedCheck.name || fetchedCheck.slug || value;
  } else if (fetchFailed) {
    // Genuinely deleted (or otherwise inaccessible) check: fall back to the
    // uid so the field stays visible and clearable instead of crashing.
    triggerLabel = value;
  } else {
    triggerLabel = "…";
    isResolving = true;
  }

  const select = (uid: string, c?: CheckType) => {
    setPickedUid(uid);
    setPickedLabel(c?.name || c?.slug || uid);
    onChange(uid, c);
    setOpen(false);
    setQuery("");
  };

  const clear = (e: React.MouseEvent) => {
    e.stopPropagation();
    onChange(undefined);
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="outline"
          disabled={disabled}
          data-testid={triggerTestId}
          className={cn(
            "h-9 w-full justify-between text-left font-normal",
            (!value || isResolving) && "text-muted-foreground",
          )}
        >
          <span className="truncate">{triggerLabel}</span>
          {value ? (
            <span
              role="button"
              tabIndex={-1}
              aria-label={t("common:clear", { defaultValue: "Clear" })}
              onClick={clear}
              className="ml-2 inline-flex items-center"
            >
              <X className="h-3.5 w-3.5 opacity-60 hover:opacity-100" />
            </span>
          ) : (
            <ChevronDown className="ml-2 h-4 w-4 opacity-60" />
          )}
        </Button>
      </PopoverTrigger>
      <PopoverContent
        align="start"
        className="w-[--radix-popover-trigger-width] p-0"
        onOpenAutoFocus={(e) => {
          e.preventDefault();
          inputRef.current?.focus();
        }}
      >
        <div className="border-b p-2">
          <Input
            ref={inputRef}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t("dependencies:searchChecks")}
            className="h-8"
            onKeyDown={(e) => {
              if (e.key === "ArrowDown") {
                e.preventDefault();
                setActiveIndex((i) => Math.min(i + 1, filtered.length - 1));
              } else if (e.key === "ArrowUp") {
                e.preventDefault();
                setActiveIndex((i) => Math.max(i - 1, 0));
              } else if (e.key === "Enter" && filtered[activeIndex]) {
                e.preventDefault();
                const c = filtered[activeIndex];
                if (c.uid) select(c.uid, c);
              } else if (e.key === "Escape") {
                e.preventDefault();
                setOpen(false);
              }
            }}
          />
        </div>
        <div className="max-h-64 overflow-y-auto p-1">
          {filtered.length === 0 ? (
            <p className="px-2 py-3 text-center text-xs text-muted-foreground">
              {t("dependencies:noMatches")}
            </p>
          ) : (
            filtered.map((c, idx) => (
              <button
                key={c.uid}
                type="button"
                onClick={() => c.uid && select(c.uid, c)}
                onMouseEnter={() => setActiveIndex(idx)}
                className={cn(
                  "flex w-full items-center justify-between gap-2 rounded-sm px-2 py-1.5 text-left text-sm",
                  idx === activeIndex && "bg-accent text-accent-foreground",
                )}
                data-testid={`check-picker-option-${c.slug ?? c.uid}`}
              >
                <span className="flex flex-1 items-center gap-2 truncate">
                  {value === c.uid && <Check className="h-3.5 w-3.5" />}
                  <span className="truncate font-medium">{c.name || c.slug}</span>
                  {c.name && c.slug && (
                    <span className="truncate text-xs text-muted-foreground">
                      {c.slug}
                    </span>
                  )}
                </span>
              </button>
            ))
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}
