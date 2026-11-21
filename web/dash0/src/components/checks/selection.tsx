import { createContext, useContext } from "react";
import { useTranslation } from "react-i18next";

import { Checkbox } from "@/components/ui/checkbox";

/**
 * The checks list's explicit row selection (spec 2026-11-21-03). It lives on
 * the page (so it survives pagination and the infinite stream) and reaches the
 * rows through this context rather than through every section's props.
 */
export interface ChecksSelection {
  selected: ReadonlySet<string>;
  toggle: (uid: string) => void;
  /** Selects (or unselects) every given check at once. */
  setMany: (uids: string[], selected: boolean) => void;
}

const emptySelection: ChecksSelection = {
  selected: new Set<string>(),
  toggle: () => undefined,
  setMany: () => undefined,
};

export const ChecksSelectionContext = createContext<ChecksSelection>(emptySelection);

export function useChecksSelection(): ChecksSelection {
  return useContext(ChecksSelectionContext);
}

/**
 * The checkbox in a checks table's header: ticks every check the table shows,
 * and is half-ticked while only some of them are selected.
 */
export function GroupSelectCheckbox({
  uids,
  disabled,
}: {
  uids: string[];
  disabled?: boolean;
}) {
  const { t } = useTranslation("checks");
  const selection = useChecksSelection();
  const selectedCount = uids.filter((uid) => selection.selected.has(uid)).length;
  const all = uids.length > 0 && selectedCount === uids.length;

  return (
    <span
      className="inline-flex shrink-0"
      onClick={(event) => event.stopPropagation()}
      onKeyDown={(event) => event.stopPropagation()}
    >
      <Checkbox
        checked={all ? true : selectedCount > 0 ? "indeterminate" : false}
        onCheckedChange={() => selection.setMany(uids, !all)}
        disabled={disabled || uids.length === 0}
        aria-label={t("bulkSelection.selectGroup")}
        data-testid="group-select-checkbox"
      />
    </span>
  );
}
