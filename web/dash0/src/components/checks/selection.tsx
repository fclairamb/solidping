import { createContext, useContext } from "react";

/**
 * The checks list's explicit row selection (spec 2026-11-21-03). It lives on
 * the page (so it survives pagination and the infinite stream) and reaches the
 * rows through this context rather than through every section's props.
 */
export interface ChecksSelection {
  selected: ReadonlySet<string>;
  toggle: (uid: string) => void;
}

const emptySelection: ChecksSelection = { selected: new Set<string>(), toggle: () => undefined };

export const ChecksSelectionContext = createContext<ChecksSelection>(emptySelection);

export function useChecksSelection(): ChecksSelection {
  return useContext(ChecksSelectionContext);
}
