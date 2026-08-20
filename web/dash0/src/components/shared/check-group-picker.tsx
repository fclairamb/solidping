import { useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Check, ChevronDown, X } from "lucide-react";

import { useCheckGroup, useCheckGroups, type CheckGroup } from "@/api/hooks";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";

export interface CheckGroupPickerProps {
  org: string;
  value?: string;
  excludeUids?: Set<string>;
  onChange: (uid: string | undefined, group?: CheckGroup) => void;
  placeholder?: string;
  disabled?: boolean;
  selectedLabel?: string;
  /** Optional data-testid for the trigger button. */
  triggerTestId?: string;
}

/**
 * Single-select picker for a check GROUP. It deliberately mirrors CheckPicker's
 * popover + search + keyboard-navigation shape (see the design reference) so the
 * two read as one control, and labels each entry with its member count —
 * "N checks" — which is the operator-side hint that the group aggregates several
 * probes. That count is dashboard-only; it is never published on the status page.
 *
 * The group list endpoint returns every group at once (there are few of them),
 * so the search box filters client-side rather than round-tripping.
 */
export function CheckGroupPicker({
  org,
  value,
  excludeUids,
  onChange,
  placeholder,
  disabled,
  selectedLabel,
  triggerTestId,
}: CheckGroupPickerProps) {
  const { t } = useTranslation(["statusPages", "checkGroups", "common"]);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [activeIndex, setActiveIndex] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);

  // Self-resolved label for `value`, independent of the caller's optional
  // `selectedLabel` override. Set synchronously on select() so the trigger
  // never flashes the raw uid between picking an entity and the next render.
  const [pickedUid, setPickedUid] = useState<string | undefined>();
  const [pickedLabel, setPickedLabel] = useState<string | undefined>();

  const { data: groups = [] } = useCheckGroups(org);

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return groups.filter((g) => {
      if (excludeUids?.has(g.uid)) return false;
      if (!needle) return true;
      return (
        g.name.toLowerCase().includes(needle) || g.slug.toLowerCase().includes(needle)
      );
    });
  }, [groups, excludeUids, query]);

  // The highlighted row is clamped at render rather than reset from an effect:
  // the list can shrink under the cursor (a keystroke narrows it, or the groups
  // query refetches), and resetting in an effect would re-render a second time
  // for every keystroke. Typing also resets it directly in the change handler,
  // which is where the state actually belongs.
  const active = Math.min(activeIndex, Math.max(filtered.length - 1, 0));

  // The group list endpoint returns every group at once, so a `value` set by
  // something other than this component's own select() almost always
  // resolves from `groups` already in hand; the single-group fetch only
  // fires when it genuinely doesn't (e.g. a deleted group).
  const needsResolve = !selectedLabel && !!value && value !== pickedUid;
  const fromList = needsResolve ? groups.find((g) => g.uid === value) : undefined;
  const {
    data: fetchedGroup,
    isError: fetchFailed,
  } = useCheckGroup(org, needsResolve && !fromList ? value! : "", {});

  let triggerLabel: string;
  let isResolving = false;
  if (selectedLabel) {
    triggerLabel = selectedLabel;
  } else if (!value) {
    triggerLabel = placeholder ?? t("statusPages:resources.selectGroup");
  } else if (value === pickedUid && pickedLabel) {
    triggerLabel = pickedLabel;
  } else if (fromList) {
    triggerLabel = fromList.name;
  } else if (fetchedGroup) {
    triggerLabel = fetchedGroup.name;
  } else if (fetchFailed) {
    // Genuinely deleted (or otherwise inaccessible) group: fall back to the
    // uid so the field stays visible and clearable instead of crashing.
    triggerLabel = value;
  } else {
    triggerLabel = "…";
    isResolving = true;
  }

  const select = (uid: string, group?: CheckGroup) => {
    setPickedUid(uid);
    setPickedLabel(group?.name ?? uid);
    onChange(uid, group);
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
            onChange={(e) => {
              setQuery(e.target.value);
              setActiveIndex(0);
            }}
            placeholder={t("statusPages:resources.searchGroups")}
            className="h-8"
            onKeyDown={(e) => {
              if (e.key === "ArrowDown") {
                e.preventDefault();
                setActiveIndex((i) => Math.min(i + 1, filtered.length - 1));
              } else if (e.key === "ArrowUp") {
                e.preventDefault();
                setActiveIndex((i) => Math.max(i - 1, 0));
              } else if (e.key === "Enter" && filtered[active]) {
                e.preventDefault();
                const g = filtered[active];
                select(g.uid, g);
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
              {t("statusPages:resources.noGroupMatches")}
            </p>
          ) : (
            filtered.map((g, idx) => (
              <button
                key={g.uid}
                type="button"
                onClick={() => select(g.uid, g)}
                onMouseEnter={() => setActiveIndex(idx)}
                className={cn(
                  "flex w-full items-center justify-between gap-2 rounded-sm px-2 py-1.5 text-left text-sm",
                  idx === active && "bg-accent text-accent-foreground",
                )}
                data-testid={`check-group-picker-option-${g.slug}`}
              >
                <span className="flex flex-1 items-center gap-2 truncate">
                  {value === g.uid && <Check className="h-3.5 w-3.5" />}
                  <span className="truncate font-medium">{g.name}</span>
                  <span className="truncate text-xs text-muted-foreground">
                    {t("statusPages:resources.groupMemberCount", {
                      count: g.checkCount,
                    })}
                  </span>
                </span>
              </button>
            ))
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}
