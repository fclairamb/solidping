import type { ReactNode } from "react";
import { ChevronDown } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";

export interface FacetedFilterOption {
  value: string;
  label: string;
}

export interface FacetedFilterProps {
  /** Full option list, one checkbox row per entry. */
  options: FacetedFilterOption[];
  /** Currently selected values. */
  selected: string[];
  onChange: (next: string[]) => void;
  /**
   * Trigger button text. Computed by the caller (not this component) so the
   * "All X" / single-value / "N selected" / "X +1" formatting stays in the
   * caller's i18n strings — see `facetedFilterTriggerLabel` in
   * `@/lib/faceted-filter`.
   */
  triggerLabel: string;
  /** data-testid on the trigger button, e.g. "status-filter". */
  testId?: string;
  icon?: ReactNode;
}

// FacetedFilter is the checks-list multi-select popover: a trigger whose
// label reflects the current selection, opening a checkbox list where each
// click toggles one option. It's the answer to "pick a value, then
// optionally another" for a small, known option set (status, check type) —
// the multi-key/multi-value sibling of LabelFilter
// (components/shared/label-filter.tsx), which handles the open-ended
// key:value label case instead.
//
// The popover is left uncontrolled: Radix already keeps it open while a
// checkbox inside is toggled and closes it on an outside click or Escape, so
// several options can be ticked in one pass without extra state here.
export function FacetedFilter({
  options,
  selected,
  onChange,
  triggerLabel,
  testId,
  icon,
}: FacetedFilterProps) {
  const selectedSet = new Set(selected);

  const toggle = (value: string) => {
    if (selectedSet.has(value)) {
      onChange(selected.filter((v) => v !== value));
    } else {
      onChange([...selected, value]);
    }
  };

  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-9 w-[160px] justify-between font-normal"
          data-testid={testId}
        >
          <span className="flex min-w-0 items-center gap-2">
            {icon}
            <span className="truncate">{triggerLabel}</span>
          </span>
          <ChevronDown className="h-4 w-4 shrink-0 opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-56 p-1">
        <div className="flex max-h-72 flex-col overflow-y-auto">
          {options.map((option) => {
            const checked = selectedSet.has(option.value);
            return (
              <label
                key={option.value}
                className="flex cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 text-sm hover:bg-accent hover:text-accent-foreground"
                data-testid={testId ? `${testId}-option-${option.value}` : undefined}
              >
                <Checkbox
                  checked={checked}
                  onCheckedChange={() => toggle(option.value)}
                />
                <span className="truncate">{option.label}</span>
              </label>
            );
          })}
        </div>
      </PopoverContent>
    </Popover>
  );
}
