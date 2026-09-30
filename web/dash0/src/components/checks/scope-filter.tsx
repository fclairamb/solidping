import { useTranslation } from "react-i18next";

import { FilterTrigger } from "@/components/shared/filter-trigger";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

export type ScopeValue = "false" | "true" | "all";

/**
 * Super-admin only: which checks the list shows (user checks, internal checks,
 * or both). Same trigger look as the other filters — dashed while on the
 * default "User checks", solid with a value badge otherwise.
 */
export function ScopeFilter({
  value,
  onChange,
}: {
  value: string;
  onChange: (value: string) => void;
}) {
  const { t } = useTranslation("checks");
  const labels: Record<ScopeValue, string> = {
    false: t("userChecks"),
    true: t("internalOnly"),
    all: t("allChecks"),
  };
  const active = value !== "false";
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <FilterTrigger
          title={t("filters.scope")}
          badges={active ? [labels[value as ScopeValue] ?? value] : []}
          count={active ? 1 : 0}
          data-testid="scope-filter"
        />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        <DropdownMenuRadioGroup value={value} onValueChange={onChange}>
          {(Object.keys(labels) as ScopeValue[]).map((key) => (
            <DropdownMenuRadioItem key={key} value={key} data-testid={`scope-filter-${key}`}>
              {labels[key]}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
