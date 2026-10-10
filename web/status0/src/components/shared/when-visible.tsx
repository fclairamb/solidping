import { useEffect, useRef, useState, type ReactNode } from "react";

interface WhenVisibleProps {
  children: ReactNode;
  /**
   * Shown until the children mount. Give it the children's exact boxes (a
   * skeleton of them) so nothing moves when they replace it.
   */
  placeholder: ReactNode;
  /**
   * How far ahead of the viewport to mount. Generous on purpose: the children
   * should be in place before they scroll into view.
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
  placeholder,
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

  // No border or padding on the wrapper, so the placeholder's own top margin
  // collapses through it exactly as the children's would.
  return <div ref={ref}>{placeholder}</div>;
}
