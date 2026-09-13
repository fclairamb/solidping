// Whether a surface may move focus into its primary input on mount.
//
// Extracted from the zero-check dashboard hero (spec 2026-09-12-04) so BOTH
// branches are testable: a Playwright viewport resize does not emulate a coarse
// pointer, so an e2e test at 375px still reports `(pointer: fine)` and can only
// ever exercise the "do focus" side of this decision.

/** The `window.matchMedia` shape this decision needs — nothing more. */
export type MatchMediaLike = (query: string) => { matches: boolean };

/**
 * Mount-focus is for pointer devices only.
 *
 * With a mouse or trackpad, landing the caret in the field a page exists for
 * costs the user nothing: no keyboard appears, nothing scrolls. On a touch
 * device the same line raises the on-screen keyboard and scrolls the page —
 * heading, explanation and all — out of view before it has been read, which is
 * the disorientation auto-focus is rightly warned about.
 *
 * When there is no `matchMedia` to ask (SSR, an ancient browser, a test double
 * that omits it), the answer is no: not focusing is the recoverable outcome.
 */
export function shouldFocusOnMount(matchMedia?: MatchMediaLike | null): boolean {
  if (typeof matchMedia !== "function") return false;
  try {
    return matchMedia("(pointer: fine)").matches;
  } catch {
    return false;
  }
}
