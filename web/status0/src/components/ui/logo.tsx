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
 * web/status0/public/logo.svg. Status0 is the public-facing surface,
 * so this component is allowed to sit next to brand-pink chrome
 * (header bar, marketing-link callouts) — see spec
 * 2026-05-09-01-brand-design-tokens-and-logo-pipeline.md.
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
