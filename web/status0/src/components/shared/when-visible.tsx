import { useEffect, useRef, useState, type ReactNode } from "react";

interface WhenVisibleProps {
  children: ReactNode;
  /** Height reserved until the children mount, close to their real height. */
  placeholderHeight: number;
  /**
   * How far ahead of the viewport to mount. Generous on purpose: the children
   * should be in place before they scroll into view, so any difference
   * between the placeholder and the real height happens off-screen.
   */
  rootMargin?: string;
}

/**
 * Mounts its children only once they come near the viewport, then keeps them
 * mounted.
 *
 * A public page of 74 resources drew 74 recharts charts in one go, about a
 * second of main-thread work before the visitor could scroll. Most of them
 * are below the fold, so they now cost nothing until the visitor gets there.
 */
export function WhenVisible({
  children,
  placeholderHeight,
  rootMargin = "800px 0px",
}: WhenVisibleProps) {
  const ref = useRef<HTMLDivElement>(null);
  const [visible, setVisible] = useState(
    () => typeof IntersectionObserver === "undefined",
  );

  useEffect(() => {
    if (visible) return;
    const el = ref.current;
    if (!el) return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) {
          setVisible(true);
          observer.disconnect();
        }
      },
      { rootMargin },
    );
    observer.observe(el);
    return () => observer.disconnect();
  }, [visible, rootMargin]);

  if (visible) return <>{children}</>;

  return (
    <div
      ref={ref}
      style={{ height: placeholderHeight }}
      aria-hidden="true"
      data-testid="deferred-placeholder"
    />
  );
}
