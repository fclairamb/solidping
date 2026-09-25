/**
 * Product analytics (PostHog) for the dashboard.
 *
 * # Off unless configured
 *
 * `posthog-js` is NEVER imported statically anywhere in the app. It is loaded
 * through a dynamic `import()` that only runs after `GET /api/v1/config`
 * reports PostHog as enabled. Vite therefore emits it as a separate chunk that
 * the browser only fetches when the feature is genuinely on: with no
 * credentials configured the dashboard downloads no analytics code and issues
 * no request to any PostHog host. There is deliberately no `<script>` tag in
 * `index.html`.
 *
 * # No field obfuscation, but no credentials either
 *
 * Autocapture, session replay and event properties are all captured in the
 * clear — no text masking, no input masking, no URL templating. A masked
 * or scrubbed event stream is not readable: reconstructing what a session
 * actually did from a redacted `elements_chain` or a templated pathname costs
 * more than the analytics are worth.
 *
 * That decision is about UI content. It never covered credentials: session
 * tokens, one-time codes and single-use links in URLs are replaced with
 * `REDACTED` by the hooks in `./analytics-redaction` (spec 2026-09-25-11),
 * and network header/body capture is pinned off, both wired in
 * `initAnalytics` below. Credentials rendered as page text are NOT masked.
 * The distinct id is still pseudonymous
 * and built from UUIDs only (see `distinctId` below) — it must match
 * `analytics.DistinctID` in `server/internal/analytics/analytics.go` exactly
 * so browser sessions and server-side events stitch together.
 */

import { redactCapturedNetworkRequest, redactCredentialsBeforeSend } from "./analytics-redaction";

/** Browser-safe PostHog settings, as returned by GET /api/v1/config. */
export interface PostHogPublicConfig {
  enabled: boolean;
  projectApiKey?: string;
  host?: string;
  uiHost?: string;
}

/**
 * Browser-safe WhatsApp capability flag, as returned by GET /api/v1/config.
 * A single resolved boolean — no credential, phone-number id or template name
 * is ever exposed to the browser.
 */
export interface WhatsAppPublicConfig {
  enabled: boolean;
}

/**
 * Browser-safe Telegram capability flag, as returned by GET /api/v1/config.
 * The bot token and the webhook secret are never exposed; the bot *username*
 * is public by nature (anyone can find the bot in Telegram) and the browser
 * needs it to build the `t.me/<botUsername>?start=<token>` connect link.
 */
export interface TelegramPublicConfig {
  enabled: boolean;
  botUsername?: string;
}

/**
 * Browser-safe view of the instance's SERVER-PROVIDED SMS and voice
 * capability, as returned by GET /api/v1/config — the mode an organization
 * gets when it has not brought its own provider account.
 *
 * `sender` and `provider` are not secrets: the sender is the string every
 * recipient's handset displays, and the provider name is what lets the UI
 * explain what "server-provided" means on this deployment. No key, token or
 * service name is ever exposed.
 *
 * `voiceEnabled` is resolved INDEPENDENTLY of `enabled`: OVHcloud has no voice
 * API, so an instance can offer OVH SMS and Twilio voice at the same time — or
 * SMS with no voice at all.
 */
export interface SMSPublicConfig {
  enabled: boolean;
  sender?: string;
  provider?: string;
  voiceEnabled: boolean;
}

/**
 * The shared public live demo, as returned by GET /api/v1/config.
 *
 * The password really is here, and really is public: the whole feature is
 * "anyone can log in and look around", and the server serves the credential
 * explicitly so this bundle does not have to hardcode a copy that drifts from
 * whatever the operator configured. Nothing is emitted at all when the demo is
 * off, so a self-hosted install advertises no account.
 */
export interface DemoPublicConfig {
  enabled: boolean;
  orgSlug?: string;
  email?: string;
  password?: string;
}

/**
 * Browser-safe Discord BOT capability flag, as returned by GET /api/v1/config.
 *
 * Deliberately not about Discord *login*: login needs a client id and secret,
 * the bot additionally needs a bot token and the application public key, and
 * production has had the first pair and not the second. The login page reads
 * GET /api/v1/auth/providers; this flag exists so the integration settings
 * panel never offers an install this deployment cannot complete.
 */
export interface DiscordPublicConfig {
  botEnabled: boolean;
}

/** The public config document. Extra keys are ignored. */
export interface PublicConfig {
  posthog?: PostHogPublicConfig;
  whatsapp?: WhatsAppPublicConfig;
  telegram?: TelegramPublicConfig;
  sms?: SMSPublicConfig;
  demo?: DemoPublicConfig;
  discord?: DiscordPublicConfig;
}

/**
 * THE enablement rule, identical to config.PostHogConfig.Active() on the
 * backend: enabled AND a non-empty project key. Anything else is off.
 */
export function isAnalyticsEnabled(config: PublicConfig | null | undefined): boolean {
  const ph = config?.posthog;
  return Boolean(ph?.enabled && ph.projectApiKey && ph.projectApiKey.trim() !== "");
}

/**
 * Builds the pseudonymous distinct id. MUST stay byte-identical to
 * analytics.DistinctID in the Go backend.
 */
export function distinctId(orgUid?: string | null, userUid?: string | null): string {
  const org = orgUid ?? "";
  const user = userUid ?? "";
  if (!org && !user) return "anonymous";
  if (!user) return `org:${org}`;
  if (!org) return `user:${user}`;
  return `org:${org}/user:${user}`;
}

// Minimal structural type for the bits of posthog-js we use. Declared locally
// so no module-level `import type` from "posthog-js" can drag the package into
// the main bundle graph.
interface PostHogLike {
  init: (key: string, options: Record<string, unknown>) => void;
  identify: (id: string, properties?: Record<string, unknown>) => void;
  reset: () => void;
  capture: (event: string, properties?: Record<string, unknown>) => void;
}

let client: PostHogLike | null = null;
let loading: Promise<PostHogLike | null> | null = null;
// Remembered so identify() calls that land before the async import resolves
// are replayed once the client is up, rather than dropped.
let pendingIdentity: string | null = null;

/**
 * Loads and initializes posthog-js — and ONLY then. Returns false without
 * importing anything when analytics is off.
 */
export async function initAnalytics(config: PublicConfig | null | undefined): Promise<boolean> {
  if (!isAnalyticsEnabled(config)) {
    // Analytics stays off: discard anything identifyAnalytics queued while the
    // config fetch was in flight. It was only ever held in memory.
    pendingIdentity = null;

    return false;
  }

  if (client) {
    return true;
  }

  const settings = config!.posthog!;

  if (!loading) {
    // Routed through ./posthog-loader so the emitted chunk carries a
    // recognizable "posthog" filename that tests can assert on.
    loading = import("./posthog-loader")
      .then((mod) => {
        const posthog = (mod.default ?? mod) as unknown as PostHogLike;
        const options: Record<string, unknown> = {
          // Defaults to the first-party proxy path so ad blockers do not drop
          // events; the backend sends an explicit host when one is configured.
          api_host: settings.host || "/ingest",
          autocapture: true,
          // Session replay, fully unmasked. It exists for ONE question the
          // event stream cannot answer — where a first-run user stalls before
          // their first check — and the 2026-09-09 signup is why:
          // reconstructing six clicks from a masked, redacted replay took an
          // evening of decoding `elements_chain`, and the three minutes he sat
          // still stayed dark. A masked replay is not a usable tool, so this
          // records layout, cursor, scroll, typed values and clicked text
          // as-is.
          disable_session_recording: false,
          // ...except credentials. The replay network plugin records the
          // document's navigation entry with its ORIGINAL URL, so the OAuth
          // handoff's `?access_token=…&refresh_token=…` reached PostHog even
          // though main.tsx strips it before we load. posthog-js also runs the
          // replay Meta `href` through this hook. See ./analytics-redaction.
          session_recording: {
            maskCapturedNetworkRequestFn: redactCapturedNetworkRequest,
            // Pinned off whatever the PostHog project says (a client-side
            // `false` wins over remote config). Supplying the mask fn above
            // disables posthog-js's own body scrubber, so bodies and headers
            // must never be recorded unless a real scrubber is added first.
            recordHeaders: false,
            recordBody: false,
          },
          // Same filter on URL-shaped event properties, so a clean
          // `$current_url` does not depend on main.tsx running first. An
          // array on purpose: other hooks (e.g. error-noise filtering) go
          // AFTER the redaction, so they only ever see redacted data.
          before_send: [redactCredentialsBeforeSend],
          // Only create person profiles for users we explicitly identify.
          person_profiles: "identified_only",
        };
        // When api_host is the first-party proxy path, posthog-js cannot derive
        // the PostHog app host, so toolbar and "view in PostHog" links need it
        // explicitly. Omitted when the backend leaves it empty.
        if (settings.uiHost) {
          options.ui_host = settings.uiHost;
        }
        posthog.init(settings.projectApiKey!.trim(), options);
        client = posthog;
        if (pendingIdentity) {
          posthog.identify(pendingIdentity);
          pendingIdentity = null;
        }
        return posthog;
      })
      .catch(() => {
        // A blocked or failed analytics chunk must never break the dashboard.
        loading = null;
        return null;
      });
  }

  return (await loading) !== null;
}

/**
 * Identifies the current session with the pseudonymous org+user id. No-op when
 * analytics was never initialized.
 */
export function identifyAnalytics(orgUid?: string | null, userUid?: string | null): void {
  if (!userUid && !orgUid) return;

  const id = distinctId(orgUid, userUid);

  if (client) {
    client.identify(id);
    return;
  }

  // Queue unconditionally, including before the /api/v1/config fetch resolves:
  // a restored session identifies almost immediately on boot, usually ahead of
  // the config round trip, and dropping it there would silently lose the
  // identity for the whole session. The id is held in memory only and is
  // replayed by initAnalytics — if analytics turns out to be off, initAnalytics
  // discards it and nothing is ever sent.
  pendingIdentity = id;
}

/** Clears the identified session on logout. No-op when analytics is off. */
export function resetAnalytics(): void {
  pendingIdentity = null;
  client?.reset();
}

/**
 * Captures a custom product event. A pure no-op when analytics was never
 * initialized (kill switch off, no credentials, or a blocked/failed load) —
 * callers never need to check `isAnalyticsEnabled` themselves.
 */
export function captureEvent(event: string, properties?: Record<string, unknown>): void {
  client?.capture(event, properties ?? {});
}

/** Test seam: forgets any loaded client. Used by unit tests only. */
export function __resetAnalyticsForTests(): void {
  client = null;
  loading = null;
  pendingIdentity = null;
}

/**
 * Fetches the public config document. Any failure (offline, 404 on an older
 * server) resolves to "analytics off" rather than rejecting.
 */
export async function fetchPublicConfig(): Promise<PublicConfig | null> {
  try {
    const response = await fetch("/api/v1/config", {
      headers: { Accept: "application/json" },
    });
    if (!response.ok) return null;
    return (await response.json()) as PublicConfig;
  } catch {
    return null;
  }
}
