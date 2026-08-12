import { Toaster as Sonner, type ToasterProps } from "sonner";

/**
 * Toasts keep the neutral popover surface and carry their meaning in the icon
 * alone — a success and an error read apart at a glance without a saturated
 * banner sliding over the UI. Sonner renders the icon as a direct `[data-icon]`
 * child with `fill="currentColor"`, so tinting the wrapper is enough; the
 * `--status-*` tokens keep it correct in both themes.
 *
 * The status hue is the only color a toast gets. Reach for `richColors` only if
 * a whole-surface treatment is ever genuinely wanted.
 *
 * Always call a typed helper. A bare `toast("…")` carries no type, and sonner
 * gives an untyped toast no icon at all — there is no lever to add one, since
 * its icon lookup is keyed on a type the toast doesn't have. A neutral message
 * is `toast.info()`, which gets the blue (i) below.
 */
const toastClassNames = {
  success: "[&>[data-icon]]:text-status-ok",
  error: "[&>[data-icon]]:text-status-error",
  warning: "[&>[data-icon]]:text-status-warning",
  info: "[&>[data-icon]]:text-primary",
};

const Toaster = ({ ...props }: ToasterProps) => {
  return (
    <Sonner
      className="toaster group"
      style={
        {
          "--normal-bg": "var(--popover)",
          "--normal-text": "var(--popover-foreground)",
          "--normal-border": "var(--border)",
        } as React.CSSProperties
      }
      toastOptions={{ classNames: toastClassNames }}
      {...props}
    />
  );
};

export { Toaster };
