/** The check types that can produce a screenshot (spec 2026-09-25-34): a
 * browser check, and a js check whose script calls page.screenshot(). */
const CAPTURE_TYPES = new Set(["browser", "js"]);

/** Whether the check page's Screenshots card applies to a check type. */
export function checkTypeCanCapture(type: string | undefined): boolean {
  return !!type && CAPTURE_TYPES.has(type);
}
