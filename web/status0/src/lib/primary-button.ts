/**
 * The solid primary button of a status page: subscribe and unlock, and
 * nothing else (spec 2026-09-24-04). Same recipe as dash0's default Button
 * (web/dash0/src/components/ui/button.tsx):
 *
 *   - `bg-primary` stays as the background-COLOR underneath
 *     `bg-primary-gradient`, so the button still reads blue if the gradient
 *     ever fails to paint;
 *   - the label sits on `--primary-gradient` in `--gradient-foreground`
 *     (white, >= 4.4:1 at every stop);
 *   - a 1px inner top highlight plus the `--primary`-tinted shadow;
 *   - the hover lift is behind `motion-safe:`.
 *
 * Every color here is a CSS variable, so an operator's custom stylesheet
 * (`--primary`, `--primary-gradient`, `--gradient-foreground`) re-themes the
 * button completely. Documented in web/docs/docs/features/status-pages.md.
 *
 * Deliberately a plain string, never passed through `cn()`: tailwind-merge
 * reads `bg-primary-gradient` as a background color and would drop the
 * `bg-primary` fallback beside it. Callers append layout classes (width,
 * padding) with a template literal.
 */
export const PRIMARY_BUTTON_CLASSES =
  "inline-flex items-center justify-center rounded-md py-2 text-sm font-medium transition bg-primary bg-primary-gradient text-gradient-foreground inset-shadow-highlight shadow-primary hover:brightness-105 hover:shadow-primary-hover motion-safe:hover:-translate-y-px disabled:pointer-events-none disabled:opacity-50";
