import { cn } from "@/lib/utils";

type LogoProps = {
  /** Pixel size of the icon mark. Default 32. */
  size?: number;
  /** "mark" = icon only; "wordmark" = icon + the SolidPing word. */
  variant?: "mark" | "wordmark";
  className?: string;
};

/**
 * Renders the SolidPing brand mark from the SVG asset shipped in
 * web/dash0/public/logo.svg. The base URL is read from
 * `import.meta.env.BASE_URL` so the same component works in dev (`/`)
 * and in production (`/dash0/`) without consumers having to know.
 */
export function Logo({ size = 32, variant = "mark", className }: LogoProps) {
  const base = import.meta.env.BASE_URL || "/";
  const src = `${base.replace(/\/$/, "")}/logo.svg`;

  return (
    <span className={cn("inline-flex items-center gap-2", className)}>
      <img
        src={src}
        alt="SolidPing"
        width={size}
        height={size}
        // The SVG is its own colored asset; don't tint it from the
        // parent (e.g. a primary-colored sidebar header) — let the
        // mark keep its brand crimson.
        style={{ width: size, height: size }}
      />
      {variant === "wordmark" && (
        <span
          className="font-semibold tracking-tight"
          style={{ fontSize: size * 0.55 }}
        >
          SolidPing
        </span>
      )}
    </span>
  );
}
