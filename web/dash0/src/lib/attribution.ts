// Inbound campaign attribution: what the marketing site tells us about where a
// visitor came from, kept until they create an account (spec 2026-09-07-03).
//
// www.solidping.io appends the ad click identifier (`gclid`, `gbraid`,
// `wbraid`, `msclkid`) and the `utm_*` tags to every link into the dashboard
// — see its src/lib/attribution.ts. This is the receiving half: read them from
// the first URL the app loads, hold them, and send them with the registration
// request so the account can be tied back to the campaign that produced it.
//
// WHY MEMORY, NOT sessionStorage. The only navigation between landing and the
// register form is in-app (`/login` → `/orgs/$org/login` → `/orgs/$org/register`
// are TanStack navigations, not reloads), so a module variable survives the
// whole path. Storing the tags would also make them the dashboard's first
// piece of advertising-related browser storage, which the public cookie
// policy would then have to declare and justify; a variable that dies with the
// tab needs no such entry. A reload loses the attribution — an accepted cost,
// and the same trade the marketing site makes.
//
// OAuth sign-ups round-trip through the provider (a full reload) and are not
// covered here; that is a separate spec. The registration request, the
// pending-registration entry and the user row all use the same JSON shape.

export type ClickIdKind = "gclid" | "gbraid" | "wbraid" | "msclkid";

export interface SignupAttribution {
  utmSource?: string;
  utmMedium?: string;
  utmCampaign?: string;
  utmTerm?: string;
  utmContent?: string;
  clickIdKind?: ClickIdKind;
  clickId?: string;
  /** Path (no query) the tagged link opened. */
  landingPath?: string;
  /** ISO-8601, when the tags were first seen. */
  capturedAt?: string;
}

const UTM_KEYS = {
  utm_source: "utmSource",
  utm_medium: "utmMedium",
  utm_campaign: "utmCampaign",
  utm_term: "utmTerm",
  utm_content: "utmContent",
} as const;

// Order matters: when a URL somehow carries two, the first listed wins.
const CLICK_ID_KEYS: readonly ClickIdKind[] = ["gclid", "gbraid", "wbraid", "msclkid"];

// The server caps at 200 too; clipping here keeps the request small and the
// contract visible on both sides.
const MAX_VALUE_LENGTH = 200;

let current: SignupAttribution | null = null;

/**
 * Parse a query string into an attribution record, or null when it carries no
 * campaign tag and no click id. Pure, for tests; `captureLandingAttribution`
 * is the stateful entry point.
 */
export function parseAttribution(
  search: string,
  pathname: string,
  now: () => Date = () => new Date(),
): SignupAttribution | null {
  const params = new URLSearchParams(search);
  const found: SignupAttribution = {};

  for (const [param, field] of Object.entries(UTM_KEYS)) {
    const value = params.get(param)?.trim();
    if (value) found[field] = value.slice(0, MAX_VALUE_LENGTH);
  }

  for (const kind of CLICK_ID_KEYS) {
    const value = params.get(kind)?.trim();
    if (value) {
      found.clickIdKind = kind;
      found.clickId = value.slice(0, MAX_VALUE_LENGTH);
      break;
    }
  }

  if (Object.keys(found).length === 0) return null;

  found.landingPath = pathname.slice(0, MAX_VALUE_LENGTH);
  found.capturedAt = now().toISOString();

  return found;
}

/**
 * Record the attribution carried by the URL the app was loaded on. Call it
 * once, synchronously, before the router mounts — the `/login` redirect
 * rewrites the URL and the tags are gone after that.
 *
 * Last tagged landing wins: an ad network attributes to the most recent
 * click, so a second tagged load in the same tab replaces the first. An
 * untagged load changes nothing.
 */
export function captureLandingAttribution(search: string, pathname: string): void {
  const parsed = parseAttribution(search, pathname);
  if (parsed) current = parsed;
}

/** The attribution to send with a registration, or undefined when none. */
export function readSignupAttribution(): SignupAttribution | undefined {
  return current ?? undefined;
}

/**
 * Forget the attribution once it has been handed to the server. The pending
 * registration carries it from here; keeping a copy would only re-attach a
 * stale click to a second account created from the same tab.
 */
export function clearSignupAttribution(): void {
  current = null;
}
