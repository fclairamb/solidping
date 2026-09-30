import type { CheckCaptureOutcome } from "@/api/hooks";

/** The check types that can produce a screenshot (spec 2026-09-25-34): a
 * browser check, a js check whose script calls page.screenshot(), an rdp check
 * with credentials and a vnc check with a password. */
const CAPTURE_TYPES = new Set(["browser", "js", "rdp", "vnc"]);

/** Whether the check page's Screenshots card applies to a check type. */
export function checkTypeCanCapture(type: string | undefined): boolean {
  return !!type && CAPTURE_TYPES.has(type);
}

/** Slack when matching a failed outcome to the pending request: both carry the
 * same microsecond instant, but a JS Date keeps milliseconds only. */
const REQUEST_MATCH_SLACK_MS = 1;

/**
 * The failure of the "Capture now" request the card is waiting on, if the
 * listing reports one (spec 2026-09-27-01). Only a failed outcome for THIS
 * request counts: an older failure (an earlier click) never ends the wait.
 */
export function pendingCaptureFailure(
  requestedAt: string,
  outcome: CheckCaptureOutcome | undefined,
): CheckCaptureOutcome | null {
  if (!outcome || !outcome.failed) return null;

  const pending = Date.parse(requestedAt);
  const answered = Date.parse(outcome.requestedAt);
  if (Number.isNaN(pending) || Number.isNaN(answered)) return null;

  return answered >= pending - REQUEST_MATCH_SLACK_MS ? outcome : null;
}
