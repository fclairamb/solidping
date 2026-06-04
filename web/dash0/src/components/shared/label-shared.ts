import { useEffect, useState } from "react";

// Shared label-editing constants and helpers used by both the label authoring
// control (`label-input.tsx`) and the checks-list faceted filter
// (`label-filter.tsx`). Keep these in one place so the two stay in lockstep.

// A label key is 3–51 chars: a leading lowercase letter then 2–50 of
// lowercase letters, digits, or hyphens.
export const KEY_REGEX = /^[a-z][a-z0-9-]{2,50}$/;
export const VALUE_MAX = 200;
export const SUGGESTION_DEBOUNCE_MS = 200;
export const SUGGESTION_LIMIT = 25;

// Human copy shown when a typed key fails KEY_REGEX. Shared so the filter and
// the authoring form word it identically.
export const KEY_ERROR =
  "Use 3–51 lowercase letters, digits, or hyphens, starting with a letter.";

// useDebounced returns a value that only updates after `delayMs` of stillness.
export function useDebounced<T>(value: T, delayMs: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setDebounced(value), delayMs);
    return () => clearTimeout(id);
  }, [value, delayMs]);
  return debounced;
}
