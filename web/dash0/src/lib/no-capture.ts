/**
 * The class posthog-js reads to keep an element out of analytics: session
 * replay blocks it (rrweb `blockClass`, the element becomes an empty
 * placeholder of the same size) and autocapture skips it and its children.
 * dash0 records sessions unmasked on purpose (see lib/analytics.ts), so this
 * class is the only thing that keeps a credential rendered as page text out of
 * the PostHog project (GHSA-c9qq-4m99-c7f6, spec 2026-11-11-05).
 */
export const NO_CAPTURE_CLASS = "ph-no-capture";

/**
 * Whether an input's props mark it as a secret field (`data-secret-input`,
 * set by the check form on credential inputs). `Input` and `Textarea` then
 * add {@link NO_CAPTURE_CLASS} so the typed value never reaches the replay.
 */
export function isSecretInput(props: object): boolean {
  const marker = (props as Record<string, unknown>)["data-secret-input"];
  return marker !== undefined && marker !== false && marker !== "false";
}
