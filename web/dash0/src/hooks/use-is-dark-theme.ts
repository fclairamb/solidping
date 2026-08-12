import { useEffect, useState } from "react";

/**
 * Tracks dash0's own light/dark theme — the `html.dark` class that `ThemeToggle`
 * flips — and re-renders when it changes.
 *
 * Needed wherever a surface can't reach the CSS custom properties and has to be
 * handed concrete colors instead: a sandboxed `<iframe srcDoc>` (opaque origin,
 * no access to the parent stylesheet), a canvas, or a third-party editor that
 * takes a `theme` prop. Style with the `--*` tokens whenever you can; reach for
 * this only when the consumer genuinely cannot see them.
 */
export function useIsDarkTheme(): boolean {
  const [dark, setDark] = useState(() =>
    typeof document === "undefined"
      ? false
      : document.documentElement.classList.contains("dark"),
  );

  useEffect(() => {
    const target = document.documentElement;
    const sync = () => setDark(target.classList.contains("dark"));
    sync();
    const observer = new MutationObserver(sync);
    observer.observe(target, { attributes: true, attributeFilter: ["class"] });
    return () => observer.disconnect();
  }, []);

  return dark;
}
