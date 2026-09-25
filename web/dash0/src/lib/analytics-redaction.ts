/**
 * Credential redaction for everything the dashboard sends to PostHog.
 *
 * Replay and events stay unmasked on purpose (see the header of
 * `analytics.ts`): typed values, clicked text and layout are recorded as-is.
 * This module is NOT a return to masking. It is a credentials filter: session
 * tokens, one-time codes and single-use links must never reach PostHog,
 * whatever page they happen to ride on.
 *
 * Why it exists (spec 2026-09-25-11): federated logins land on
 * `/d/orgs/<slug>?access_token=…&refresh_token=…`. main.tsx strips the params
 * with `history.replaceState`, but the replay network plugin records the
 * document's navigation entry, which keeps the original URL. Refresh tokens do
 * not rotate, so a recorded URL was a session takeover for anyone with read
 * access to the PostHog project.
 *
 * The two hooks wired by `initAnalytics`:
 * - {@link redactCapturedNetworkRequest}: `session_recording.maskCapturedNetworkRequestFn`.
 *   posthog-js calls it for every recorded network entry (navigation entry
 *   included) and also for the rrweb Meta `href` and replay `$url_changed` /
 *   `$pageview` custom events, so one hook covers every URL replay stores.
 *   It does NOT look at page content: a credential rendered as text on the
 *   page (a new PAT, a heartbeat URL, a TOTP secret) is still in the DOM
 *   snapshot.
 * - {@link redactCredentialsBeforeSend}: the first `before_send` hook. Applies
 *   the same filter to the URL-shaped event properties.
 *
 * Composing (spec 2026-09-25-13 and later): `before_send` is an array. Keep
 * `redactCredentialsBeforeSend` FIRST and append other hooks after it, so they
 * only ever see redacted data. To cover a new token-bearing param, add it to
 * {@link CREDENTIAL_QUERY_PARAMS}; for a new single-use link route, add its
 * path segment to {@link CREDENTIAL_PATH_SEGMENTS}. Use {@link redactUrl}
 * directly for any other URL-bearing value.
 *
 * Pure module: no posthog-js import (that would drag the package into the main
 * bundle, see posthog-loader.ts), no DOM access.
 */

/** The value every redacted credential is replaced with. */
export const REDACTED = "REDACTED";

/**
 * Query (and param-shaped fragment) keys whose value is a credential. Matched
 * case-insensitively.
 */
export const CREDENTIAL_QUERY_PARAMS: readonly string[] = [
  "access_token",
  "refresh_token",
  "token",
  "code",
  "state",
  "tempToken",
  "id_token",
];

/**
 * Path segments followed by a single-use credential (`/reset-password/<token>`,
 * `/invite/<token>` and its `/api/v1/auth/invite/<token>` lookup,
 * `/confirm-registration/<token>`). The segment after them is redacted.
 */
export const CREDENTIAL_PATH_SEGMENTS: readonly string[] = [
  "reset-password",
  "invite",
  "confirm-registration",
];

/** Captured headers dropped outright. Compared lowercased. */
const CREDENTIAL_HEADERS = new Set([
  "authorization",
  "proxy-authorization",
  "cookie",
  "set-cookie",
]);

const credentialParams = new Set(CREDENTIAL_QUERY_PARAMS.map((p) => p.toLowerCase()));

const credentialPathRe = new RegExp(
  `(^|/)(${CREDENTIAL_PATH_SEGMENTS.map((s) => s.replace(/[-]/g, "\\-")).join("|")})/([^/?#]+)`,
  "gi",
);

function safeDecode(value: string): string {
  try {
    return decodeURIComponent(value.replace(/\+/g, " "));
  } catch {
    return value;
  }
}

/**
 * Redacts the credential params of an `a=b&c=d` string, leaving every other
 * segment byte-for-byte as it was.
 */
function redactParamString(params: string): string {
  if (!params.includes("=")) return params;

  return params
    .split("&")
    .map((segment) => {
      const eq = segment.indexOf("=");
      if (eq < 0) return segment;

      const rawKey = segment.slice(0, eq);
      const rawValue = segment.slice(eq + 1);
      if (credentialParams.has(safeDecode(rawKey).toLowerCase())) {
        return `${rawKey}=${REDACTED}`;
      }

      // A harmless param can still carry a token-bearing URL, percent-encoded
      // (e.g. `returnTo=%2Fd%2Forgs%2Facme%3Faccess_token%3D…`). Only rewrite
      // it when there is something to redact, so ordinary values keep their
      // exact encoding.
      const decoded = safeDecode(rawValue);
      if (decoded !== rawValue) {
        const redacted = redactUrl(decoded);
        if (redacted !== decoded) return `${rawKey}=${encodeURIComponent(redacted)}`;
      }
      return segment;
    })
    .join("&");
}

function redactPath(path: string): string {
  return path.replace(credentialPathRe, `$1$2/${REDACTED}`);
}

/**
 * Replaces the value of every credential query param in `url` with
 * `REDACTED`. Works on absolute and relative URLs and on bare query strings,
 * and never normalizes anything it does not redact.
 *
 * - `?access_token=…&org=acme` → `?access_token=REDACTED&org=acme`
 * - repeated params are all redacted
 * - a param-shaped fragment (`#access_token=…`, `#/path?token=…`) is filtered
 *   too; a plain fragment (`#section`) is left alone
 * - the single-use token after `/reset-password/`, `/invite/` and
 *   `/confirm-registration/` is redacted
 */
export function redactUrl(url: string): string {
  if (typeof url !== "string" || url === "") return url;

  const hashAt = url.indexOf("#");
  const beforeHash = hashAt < 0 ? url : url.slice(0, hashAt);
  const fragment = hashAt < 0 ? null : url.slice(hashAt + 1);

  const queryAt = beforeHash.indexOf("?");
  const path = queryAt < 0 ? beforeHash : beforeHash.slice(0, queryAt);
  const query = queryAt < 0 ? null : beforeHash.slice(queryAt + 1);

  let out = redactPath(path);
  if (query !== null) out += `?${redactParamString(query)}`;

  if (fragment !== null) {
    // Hash routes can carry their own query (`#/path?token=…`).
    const fragQueryAt = fragment.indexOf("?");
    out +=
      "#" +
      (fragQueryAt < 0
        ? redactParamString(fragment)
        : `${fragment.slice(0, fragQueryAt)}?${redactParamString(fragment.slice(fragQueryAt + 1))}`);
  }

  return out;
}

function redactHeaders(
  headers: Record<string, string> | undefined,
): Record<string, string> | undefined {
  if (!headers) return headers;
  const out: Record<string, string> = {};
  for (const [name, value] of Object.entries(headers)) {
    if (!CREDENTIAL_HEADERS.has(name.toLowerCase())) out[name] = value;
  }
  return out;
}

/**
 * Structural subset of posthog-js's `CapturedNetworkRequest` that this hook
 * touches. posthog-js sometimes calls the hook with only `{ name }` (replay
 * Meta / URL-change events), so every field is optional.
 */
export interface CapturedRequestLike {
  name?: string;
  url?: string;
  requestHeaders?: Record<string, string>;
  responseHeaders?: Record<string, string>;
  requestBody?: string | null;
  responseBody?: string | null;
}

/**
 * `session_recording.maskCapturedNetworkRequestFn`. Always returns the
 * request (returning null would silently drop the entry from the replay),
 * with its URL redacted, credential headers dropped and any body removed.
 *
 * Header and body capture are pinned off client-side in `initAnalytics`
 * (`recordHeaders: false`, `recordBody: false`, which posthog-js honours over
 * the project settings). The header/body handling below is a second layer,
 * not a licence to turn capture back on: enabling body capture needs a real
 * body scrubber first.
 */
export function redactCapturedNetworkRequest<T extends CapturedRequestLike>(request: T): T {
  if (!request || typeof request !== "object") return request;

  const out: CapturedRequestLike = { ...request };
  if (typeof out.name === "string") out.name = redactUrl(out.name);
  if (typeof out.url === "string") out.url = redactUrl(out.url);
  if (out.requestHeaders) out.requestHeaders = redactHeaders(out.requestHeaders);
  if (out.responseHeaders) out.responseHeaders = redactHeaders(out.responseHeaders);
  // Bodies fail closed. Supplying maskCapturedNetworkRequestFn turns OFF
  // posthog-js's built-in body scrubber (scrubPayloads), and a key-name
  // filter here would miss things like `recoveryCodes`, `signingSecret` or a
  // heartbeat URL inside a JSON value. initAnalytics pins recordBody: false,
  // so nothing should arrive here; if a body ever does, it is dropped.
  if ("requestBody" in out) out.requestBody = undefined;
  if ("responseBody" in out) out.responseBody = undefined;
  return out as T;
}

/**
 * The URL-shaped event properties posthog-js (and our own captures) attach.
 * Some live in `properties`, the `$initial_*` ones mostly in `$set_once`, so
 * all three maps are filtered.
 */
export const URL_EVENT_PROPERTIES: readonly string[] = [
  "$current_url",
  "$referrer",
  "$initial_current_url",
  "$initial_referrer",
  "$session_entry_url",
  "$session_entry_referrer",
  "$pathname",
  "$initial_pathname",
  "$session_entry_pathname",
  "$prev_pageview_pathname",
  "$external_click_url",
];

/** Structural subset of posthog-js's `CaptureResult`. */
export interface CaptureResultLike {
  event?: string;
  properties?: Record<string, unknown>;
  $set?: Record<string, unknown>;
  $set_once?: Record<string, unknown>;
  [key: string]: unknown;
}

function redactUrlProperties(
  props: Record<string, unknown> | undefined,
): Record<string, unknown> | undefined {
  if (!props || typeof props !== "object") return props;
  let out: Record<string, unknown> | null = null;
  for (const key of URL_EVENT_PROPERTIES) {
    const value = props[key];
    if (typeof value !== "string") continue;
    const redacted = redactUrl(value);
    if (redacted !== value) {
      out ??= { ...props };
      out[key] = redacted;
    }
  }
  return out ?? props;
}

/**
 * `before_send` hook: applies {@link redactUrl} to the URL-shaped properties
 * of every event (`properties`, `$set`, `$set_once`). Never drops an event.
 * Keep it first in the `before_send` array (see the module header).
 */
export function redactCredentialsBeforeSend<T extends CaptureResultLike>(event: T | null): T | null {
  if (!event) return event;
  const out: CaptureResultLike = { ...event };
  if (out.properties) out.properties = redactUrlProperties(out.properties);
  if (out.$set) out.$set = redactUrlProperties(out.$set);
  if (out.$set_once) out.$set_once = redactUrlProperties(out.$set_once);
  return out as T;
}
