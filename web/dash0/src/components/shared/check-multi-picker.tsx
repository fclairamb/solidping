import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Check, ChevronDown, X } from "lucide-react";

import { useChecks, useChecksByUids, useCheckGroups } from "@/api/hooks";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";

const SEARCH_DEBOUNCE_MS = 150;
const SEARCH_LIMIT = 25;

export interface CheckMultiPickerProps {
  org: string;
  // "checks" picks from the org's checks; "groups" from its check groups.
  kind: "checks" | "groups";
  value: string[];
  onChange: (uids: string[]) => void;
  placeholder?: string;
  disabled?: boolean;
  "data-testid"?: string;
}

// A multi-select for checks or check groups, rendering the current selection as
// removable Badge chips. Mirrors the single-value CheckPicker (Popover + search
// list) but accumulates UIDs. Used by the maintenance-window form, which needs
// to associate multiple checks AND multiple groups.
export function CheckMultiPicker({
  org,
  kind,
  value,
  onChange,
  placeholder,
  disabled,
  "data-testid": testid,
}: CheckMultiPickerProps) {
  const { t } = useTranslation(["dependencies", "common"]);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [debouncedQuery, setDebouncedQuery] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const handle = setTimeout(() => setDebouncedQuery(query), SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(handle);
  }, [query]);

  // Checks support server-side search; groups are fetched whole and filtered
  // client-side (the org's group list is small).
  const { data: checkMatches = [] } = useChecks(
    org,
    kind === "checks" ? { q: debouncedQuery || undefined, limit: SEARCH_LIMIT } : undefined,
  );
  const { data: groups = [] } = useCheckGroups(org);

  // Normalize both sources to a {uid, label, secondary} option shape.
  const options = useMemo(() => {
    if (kind === "checks") {
      return checkMatches
        .filter((c) => c.uid)
        .map((c) => ({
          uid: c.uid,
          label: c.name || c.slug || c.uid,
          secondary: c.name && c.slug ? c.slug : undefined,
        }));
    }
    const q = debouncedQuery.toLowerCase();
    return groups
      .filter((g) => !q || g.name.toLowerCase().includes(q) || g.slug.toLowerCase().includes(q))
      .map((g) => ({ uid: g.uid, label: g.name, secondary: g.slug }));
  }, [kind, checkMatches, groups, debouncedQuery]);

  // Selected checks that the current search page doesn't cover (an edit form
  // loading a persisted selection, or a narrowed search that excludes a
  // previously-shown chip). Resolved individually below, mirroring
  // CheckPicker's single-uid fallback (check-picker.tsx:64-100). Not
  // applicable to "groups": groups are fetched whole, so `groups` above
  // already contains every uid.
  const unresolvedCheckUids = useMemo(() => {
    if (kind !== "checks") return [];
    const known = new Set(checkMatches.filter((c) => c.uid).map((c) => c.uid));
    return value.filter((uid) => uid && !known.has(uid));
  }, [kind, value, checkMatches]);

  // One query per unresolved uid, sharing useCheck's cache key/fetch so this
  // is a no-op request whenever the check was already fetched elsewhere.
  const resolvedQueries = useChecksByUids(org, unresolvedCheckUids);

  // Every label knowable from THIS render's own data: current search page,
  // the full group list, and any individually-resolved checks.
  const liveLabels = useMemo(() => {
    const map = new Map<string, string>();
    for (const o of options) map.set(o.uid, o.label);
    if (kind === "groups") {
      for (const g of groups) map.set(g.uid, g.name);
    } else {
      for (const c of checkMatches) {
        if (c.uid) map.set(c.uid, c.name || c.slug || c.uid);
      }
      unresolvedCheckUids.forEach((uid, i) => {
        const check = resolvedQueries[i]?.data;
        if (check) map.set(uid, check.name || check.slug || uid);
      });
    }
    return map;
  }, [options, groups, kind, checkMatches, unresolvedCheckUids, resolvedQueries]);

  // Sticky label cache: once a uid resolves to a real name it must never
  // regress — not even to the "…" placeholder — when a later, narrower
  // search excludes it from `checkMatches`. Adjusted directly in the render
  // body (React's documented "adjust state while rendering" pattern —
  // guarded by the identity check below, so it only re-renders when a label
  // actually changed) rather than in an effect: this repo's react-compiler
  // lint config (react-hooks/set-state-in-render's sibling
  // set-state-in-effect) flags a synchronous setState inside a bare effect
  // as a cascading-render anti-pattern. `nextStickyLabels` (this render's
  // merged value, not the possibly-stale `stickyLabels` state) is what
  // `labelForChip` reads below, so a label resolving THIS render is still
  // reflected in THIS paint.
  const [stickyLabels, setStickyLabels] = useState<Map<string, string>>(new Map());
  let nextStickyLabels = stickyLabels;
  for (const [uid, label] of liveLabels) {
    if (nextStickyLabels.get(uid) !== label) {
      if (nextStickyLabels === stickyLabels) nextStickyLabels = new Map(stickyLabels);
      nextStickyLabels.set(uid, label);
    }
  }
  if (nextStickyLabels !== stickyLabels) setStickyLabels(nextStickyLabels);

  // Display label for a chip: live data wins when available, then the
  // sticky cache; while a "checks" uid is still being individually
  // resolved, show a placeholder; if that fetch failed (deleted/inaccessible
  // check), fall back to the raw uid so the chip stays visible and
  // removable — matching check-picker.tsx:93-96.
  const labelForChip = (uid: string): string => {
    const live = liveLabels.get(uid);
    if (live) return live;
    const sticky = nextStickyLabels.get(uid);
    if (sticky) return sticky;
    if (kind === "checks") {
      const idx = unresolvedCheckUids.indexOf(uid);
      if (idx !== -1) return resolvedQueries[idx]?.isError ? uid : "…";
    }
    return uid;
  };

  const selectedSet = useMemo(() => new Set(value), [value]);

  const toggle = (uid: string) => {
    if (selectedSet.has(uid)) {
      onChange(value.filter((v) => v !== uid));
    } else {
      onChange([...value, uid]);
    }
  };

  const remove = (uid: string) => onChange(value.filter((v) => v !== uid));

  const triggerLabel =
    placeholder ??
    (kind === "checks"
      ? t("dependencies:pickCheck", { defaultValue: "Select checks" })
      : t("common:select", { defaultValue: "Select groups" }));

  return (
    <div className="space-y-2" data-testid={testid}>
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <Button
            type="button"
            variant="outline"
            disabled={disabled}
            className="h-9 w-full justify-between text-left font-normal text-muted-foreground"
          >
            <span className="truncate">{triggerLabel}</span>
            <ChevronDown className="ml-2 h-4 w-4 opacity-60" />
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
              placeholder={t("dependencies:searchChecks", { defaultValue: "Search..." })}
              className="h-8"
            />
          </div>
          <div className="max-h-64 overflow-y-auto p-1">
            {options.length === 0 ? (
              <p className="px-2 py-3 text-center text-xs text-muted-foreground">
                {t("dependencies:noMatches", { defaultValue: "No matches" })}
              </p>
            ) : (
              options.map((o) => {
                const selected = selectedSet.has(o.uid);
                return (
                  <button
                    key={o.uid}
                    type="button"
                    onClick={() => toggle(o.uid)}
                    className={cn(
                      "flex w-full items-center justify-between gap-2 rounded-sm px-2 py-1.5 text-left text-sm hover:bg-accent hover:text-accent-foreground",
                      selected && "bg-accent/50",
                    )}
                    data-testid={`${testid ?? "multi-picker"}-option-${o.uid}`}
                  >
                    <span className="flex flex-1 items-center gap-2 truncate">
                      <Check
                        className={cn("h-3.5 w-3.5 shrink-0", selected ? "opacity-100" : "opacity-0")}
                      />
                      <span className="truncate font-medium">{o.label}</span>
                      {o.secondary && (
                        <span className="truncate text-xs text-muted-foreground">
                          {o.secondary}
                        </span>
                      )}
                    </span>
                  </button>
                );
              })
            )}
          </div>
        </PopoverContent>
      </Popover>

      {value.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {value.map((uid) => (
            <Badge key={uid} variant="secondary" className="gap-1 pr-1">
              <span className="truncate max-w-[12rem]">{labelForChip(uid)}</span>
              <button
                type="button"
                onClick={() => remove(uid)}
                aria-label={t("common:remove", { defaultValue: "Remove" })}
                className="inline-flex items-center rounded-sm hover:bg-muted"
                data-testid={`${testid ?? "multi-picker"}-chip-remove-${uid}`}
              >
                <X className="h-3 w-3" />
              </button>
            </Badge>
          ))}
        </div>
      )}
    </div>
  );
}
