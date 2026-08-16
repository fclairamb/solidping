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
 * # Privacy
 *
 * - The distinct id is pseudonymous and built from UUIDs only — it must match
 *   `analytics.DistinctID` in `server/internal/analytics/analytics.go` exactly
 *   so browser sessions and server-side events stitch together.
 * - Autocapture masks input values and element attributes.
 * - SolidPing URLs embed org slugs and check UIDs, so every captured URL and
 *   pathname is rewritten to a route template before it leaves the browser.
 */

/** Browser-safe PostHog settings, as returned by GET /api/v1/config. */
export interface PostHogPublicConfig {
  enabled: boolean;
  projectApiKey?: string;
  host?: string;
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

/** The public config document. Extra keys are ignored. */
export interface PublicConfig {
  posthog?: PostHogPublicConfig;
  whatsapp?: WhatsAppPublicConfig;
  telegram?: TelegramPublicConfig;
  sms?: SMSPublicConfig;
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

const UUID_RE =
  /[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/g;

/**
 * Rewrites a dashboard pathname into a route template. Org slugs and resource
 * identifiers are identifying/sensitive, so they never leave the browser:
 * `/dash0/orgs/acme-corp/checks/8f0e…` becomes `/dash0/orgs/:org/checks/:uid`.
 */
export function scrubPath(path: string): string {
  return path
    .replace(/\/orgs\/[^/]+/g, "/orgs/:org")
    .replace(UUID_RE, ":uid")
    .replace(
      /\/(checks|incidents|status-pages|integrations|maintenance-windows|escalation-policies|on-call|check-groups|groups|members|tokens|labels|severities|agents|files|jobs|results)\/(?!:)[^/?#]+/g,
      "/$1/:id",
    );
}

/** Rewrites a full URL to origin + scrubbed pathname, dropping query and hash. */
export function scrubUrl(rawUrl: string): string {
  try {
    const url = new URL(rawUrl);
    return `${url.origin}${scrubPath(url.pathname)}`;
  } catch {
    return scrubPath(rawUrl);
  }
}

const URL_PROPERTY_KEYS = [
  "$current_url",
  "$referrer",
  "$initial_current_url",
  "$initial_referrer",
];

/**
 * Strips identifying path segments out of every URL-ish autocapture property
 * before the event is queued.
 */
export function sanitizeProperties(
  properties: Record<string, unknown>,
): Record<string, unknown> {
  const out = { ...properties };

  for (const key of URL_PROPERTY_KEYS) {
    const value = out[key];
    if (typeof value === "string" && value !== "" && value !== "$direct") {
      out[key] = scrubUrl(value);
    }
  }

  if (typeof out.$pathname === "string") {
    out.$pathname = scrubPath(out.$pathname);
  }

  return out;
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
        posthog.init(settings.projectApiKey!.trim(), {
          api_host: settings.host || "https://eu.i.posthog.com",
          // Conservative autocapture: never ship typed values or element
          // attributes, and never record sessions.
          autocapture: true,
          mask_all_element_attributes: true,
          mask_all_text: true,
          disable_session_recording: true,
          // Only create person profiles for users we explicitly identify.
          person_profiles: "identified_only",
          // Scrub org slugs / resource UIDs out of every captured URL.
          sanitize_properties: sanitizeProperties,
        });
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
