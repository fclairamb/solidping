import { useCallback, useSyncExternalStore } from "react";

/**
 * Tracks a CSS media query. Returns false where `matchMedia` is unavailable
 * (tests, very old browsers), so callers should pick the phone layout as the
 * "false" branch.
 *
 * Reach for this only when one element must MOVE between breakpoints (e.g. a
 * timestamp that lives in a different table cell on phones) and rendering it
 * twice with `hidden sm:block` would duplicate a data-testid or a live
 * region. For plain show/hide, Tailwind's responsive classes are simpler.
 */
export function useMediaQuery(query: string): boolean {
  const subscribe = useCallback(
    (onChange: () => void) => {
      if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
        return () => {};
      }
      const mql = window.matchMedia(query);
      mql.addEventListener("change", onChange);
      return () => mql.removeEventListener("change", onChange);
    },
    [query],
  );

  const getSnapshot = () =>
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia(query).matches;

  return useSyncExternalStore(subscribe, getSnapshot, () => false);
}
