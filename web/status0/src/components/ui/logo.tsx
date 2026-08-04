import type { CSSProperties } from "react";
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
 *
 * The size is published as the `--sp-logo-size` custom property and applied
 * from index.css (`.sp-logo`, `.sp-logo img`) rather than as an inline style on
 * the <img>: an inline declaration cannot be overridden by an operator's custom
 * stylesheet without `!important`, and replacing the logo purely in CSS is a
 * documented feature — see web/docs/docs/features/status-pages.md.
 */
export function Logo({ size = 32, variant = "mark", className }: LogoProps) {
  const base = import.meta.env.BASE_URL || "/";
  const src = `${base.replace(/\/$/, "")}/logo.svg`;

  return (
    <span
      className={cn("sp-logo inline-flex items-center gap-2", className)}
      style={{ "--sp-logo-size": `${size}px` } as CSSProperties}
    >
      <img src={src} alt="SolidPing" width={size} height={size} />
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
