import { useTranslation } from "react-i18next";
import { FilterTrigger } from "@/components/shared/filter-trigger";
import { facetedFilterBadges } from "@/lib/faceted-filter";
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
   * The dimension name shown on the trigger ("Status", "Type"). The selected
   * option labels are appended as value badges, see `facetedFilterBadges`.
   */
  title: string;
  /** data-testid on the trigger button, e.g. "status-filter". */
  testId?: string;
}

// FacetedFilter is the checks-list multi-select popover: a trigger showing the
// dimension name and the current selection as value badges, opening a checkbox list where each
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
  title,
  testId,
}: FacetedFilterProps) {
  const { t } = useTranslation("checks");
  const selectedSet = new Set(selected);
  const badges = facetedFilterBadges(selected, options, (count) =>
    t("filters.selected", { count }),
  );

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
        <FilterTrigger
          title={title}
          badges={badges}
          count={selected.filter((v) => options.some((o) => o.value === v)).length}
          data-testid={testId}
        />
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
