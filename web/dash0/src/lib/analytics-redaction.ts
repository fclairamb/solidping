/**
 * Credential redaction for everything the dashboard sends to PostHog.
 *
 * Replay and events stay unmasked on purpose (see the header of
 * `analytics.ts`): typed values, clicked text and layout are recorded as-is.
 * This module is NOT a return to masking. It is a credentials filter: session
 * tokens, one-time codes and single-use links must never reach PostHog,
 * whatever page they happen to ride on.
 *
 * Why it exists (spec 2026-09-25-11): federated logins landed on
 * `/d/orgs/<slug>?access_token=…&refresh_token=…` (since spec 2026-09-25-12
 * they land on `/d/auth/complete?code=…` with a single-use code instead, and
 * the old shape only survives a rolling deploy). main.tsx strips the params
 * with `history.replaceState`, but the replay network plugin records the
 * document's navigation entry, which keeps the original URL. Refresh tokens do
 * not rotate, so a recorded URL was a session takeover for anyone with read
 * access to the PostHog project.
 *
 * The hooks wired by `initAnalytics`:
 * - {@link redactCapturedNetworkRequest}: `session_recording.maskCapturedNetworkRequestFn`.
 *   posthog-js calls it for every recorded network entry (navigation entry
 *   included) and also for the rrweb Meta `href` and replay `$url_changed` /
 *   `$pageview` custom events, so one hook covers every URL replay stores.
 *   It does NOT look at page content: a credential rendered as text on the
 *   page (a new PAT, a heartbeat URL, a TOTP secret) is still in the DOM
 *   snapshot.
 * - {@link redactCredentialsBeforeSend}: the first `before_send` hook. Applies
 *   the same filter to the URL-shaped event properties AND, since spec
 *   2026-09-25-13 turned on exception autocapture, to `$exception` events:
 *   every exception's `value` (the message — free text, may EMBED a
 *   credential URL rather than BE one, e.g. a failed fetch to
 *   `/d/auth/complete?code=…`) and every stack frame's `filename` /
 *   `abs_path`.
 *
 * Composing (spec 2026-09-25-13 and later): `before_send` is an array. Keep
 * `redactCredentialsBeforeSend` FIRST and append other hooks after it, so they
 * only ever see redacted data. To cover a new token-bearing param, add it to
 * {@link CREDENTIAL_QUERY_PARAMS}; for a new single-use link route, add its
 * path segment to {@link CREDENTIAL_PATH_SEGMENTS}. Use {@link redactUrl}
 * directly for a value known to BE a URL, or {@link redactCredentialsInText}
 * for free text that may merely CONTAIN one.
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

// Free-text counterpart of `credentialParams`: matches a credential param
// name as a whole word (so `tempToken=` still matches via its own
// alternative, but scanning for `token` never fires inside it) followed by
// `=` and a value, wherever it appears in a string — not just inside a
// parsed query string.
const freeTextCredentialParamRe = new RegExp(
  `\\b(${CREDENTIAL_QUERY_PARAMS.join("|")})=[^&\\s"'<>)\\]]*`,
  "gi",
);

/**
 * Redacts credential-shaped substrings inside free text. Unlike
 * {@link redactUrl}, this does NOT assume the whole string is a URL — it
 * finds every occurrence of a credential query param (`access_token=…`) or a
 * single-use path segment (`/reset-password/…`) anywhere in `text` and
 * redacts just that value, leaving everything else byte-for-byte.
 *
 * For exception messages built from `console.error("...", someError)`,
 * posthog-js's console wrapper uses the `Error` object itself when one of
 * the arguments is one (see the module header), so this mostly matters for
 * messages that embed a URL directly, e.g. `TypeError: Failed to fetch
 * /d/auth/complete?code=abc123` or `[auth] token refresh failed:
 * https://…/reset-password/rp_abc123`.
 */
export function redactCredentialsInText(text: string): string {
  if (typeof text !== "string" || text === "") return text;
  let out = text.replace(credentialPathRe, `$1$2/${REDACTED}`);
  out = out.replace(freeTextCredentialParamRe, `$1=${REDACTED}`);
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
 * Structural subset of a stack frame (`$exception_list[].stacktrace.frames`)
 * that this hook touches. `filename` and `abs_path` are genuine URLs (the
 * source file the frame ran from), unlike the exception `value` below.
 */
interface StackFrameLike {
  filename?: unknown;
  abs_path?: unknown;
  [key: string]: unknown;
}

/**
 * Structural subset of an `Exception` from `$exception_list` (posthog-js /
 * `@posthog/core`'s error-tracking types).
 */
interface ExceptionLike {
  type?: unknown;
  value?: unknown;
  stacktrace?: { frames?: StackFrameLike[]; [key: string]: unknown };
  [key: string]: unknown;
}

const STACK_FRAME_URL_KEYS = ["filename", "abs_path"] as const;

function redactStackFrame(frame: StackFrameLike): StackFrameLike {
  if (!frame || typeof frame !== "object") return frame;
  let out: StackFrameLike | null = null;
  for (const key of STACK_FRAME_URL_KEYS) {
    const value = frame[key];
    if (typeof value !== "string") continue;
    const redacted = redactUrl(value);
    if (redacted !== value) {
      out ??= { ...frame };
      out[key] = redacted;
    }
  }
  return out ?? frame;
}

function redactException(exception: ExceptionLike): ExceptionLike {
  if (!exception || typeof exception !== "object") return exception;

  let value = exception.value;
  let valueChanged = false;
  if (typeof value === "string") {
    const redacted = redactCredentialsInText(value);
    if (redacted !== value) {
      value = redacted;
      valueChanged = true;
    }
  }

  const frames = exception.stacktrace?.frames;
  let redactedFrames: StackFrameLike[] | undefined;
  let framesChanged = false;
  if (Array.isArray(frames)) {
    redactedFrames = frames.map((frame) => {
      const out = redactStackFrame(frame);
      if (out !== frame) framesChanged = true;
      return out;
    });
  }

  if (!valueChanged && !framesChanged) return exception;

  return {
    ...exception,
    ...(valueChanged ? { value } : {}),
    ...(framesChanged && exception.stacktrace
      ? { stacktrace: { ...exception.stacktrace, frames: redactedFrames } }
      : {}),
  };
}

/**
 * Redacts every exception's `value` (free text — see
 * {@link redactCredentialsInText}) and stack frame `filename` / `abs_path`
 * (genuine URLs — see {@link redactUrl}) in a `$exception_list`. Never
 * changes the list's length or order.
 */
function redactExceptionList(list: unknown): unknown {
  if (!Array.isArray(list)) return list;
  let changed = false;
  const out = list.map((exception: ExceptionLike) => {
    const redacted = redactException(exception);
    if (redacted !== exception) changed = true;
    return redacted;
  });
  return changed ? out : list;
}

/**
 * `before_send` hook: applies {@link redactUrl} to the URL-shaped properties
 * of every event (`properties`, `$set`, `$set_once`), and — since spec
 * 2026-09-25-13 turned exception autocapture on — redacts `properties.$exception_list`
 * for `$exception` events (see {@link redactExceptionList}). Never drops an
 * event. Keep it first in the `before_send` array (see the module header).
 */
export function redactCredentialsBeforeSend<T extends CaptureResultLike>(event: T | null): T | null {
  if (!event) return event;
  const out: CaptureResultLike = { ...event };
  if (out.properties) {
    let props = redactUrlProperties(out.properties);
    if (Array.isArray(props?.$exception_list)) {
      const redactedList = redactExceptionList(props.$exception_list);
      if (redactedList !== props.$exception_list) {
        props = props === out.properties ? { ...props } : props;
        props.$exception_list = redactedList;
      }
    }
    out.properties = props;
  }
  if (out.$set) out.$set = redactUrlProperties(out.$set);
  if (out.$set_once) out.$set_once = redactUrlProperties(out.$set_once);
  return out as T;
}
