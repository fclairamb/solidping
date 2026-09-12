// Target-string rules for the zero-check dashboard hero's quick-create form
// (components/dashboard/empty-state-onboarding.tsx), extracted so they can be
// unit-tested without rendering React.
//
// Spec 2026-09-12-04: the hero used to wipe the input on every chip click
// (`setValue("")`), which destroyed a typed hostname even though a bare
// hostname is a perfectly good target for ALL THREE quick types. These helpers
// answer the two questions that replaced that unconditional clear:
//
//   - does what the user already typed still apply to the type they just
//     picked? (`targetAppliesTo`)
//   - if they submit it, what exactly do we send? (`normalizeTarget`)

/** The three check types the hero's quick-start chips offer. */
export type QuickCheckType = "http" | "icmp" | "ssl";

/** Why a target is not submittable, or `null` when it is. */
export type QuickTargetProblem = "empty" | "invalid";

// A scheme ("https://", "ping://"), a path/query/fragment, or whitespace. Any
// of these means the string is a URL (or a typo), not the plain host name that
// `icmp.host` / `ssl.host` expect. A colon is deliberately NOT in here: it is
// legal in an IPv6 literal and in a `host:port` pair, neither of which is a
// reason to throw the user's typing away.
const NOT_A_BARE_HOST = /^[a-zA-Z][a-zA-Z0-9+.-]*:\/\/|[/?#]|\s/;

/**
 * Whether `raw` can still be used as the target of `type`.
 *
 * A bare hostname (`example.com`) applies to every quick type — that is the
 * common case, and it must survive a chip switch. A full URL only applies to
 * `http`; moving `https://example.com` to Ping is the one case where clearing
 * the input is the right thing to do.
 *
 * An empty value trivially "applies" (there is nothing to preserve or discard).
 */
export function targetAppliesTo(type: QuickCheckType, raw: string): boolean {
  const value = raw.trim();
  if (!value) return true;
  // http takes both a full URL and a bare hostname (normalizeTarget adds the
  // scheme the backend insists on).
  if (type === "http") return true;
  return !NOT_A_BARE_HOST.test(value);
}

/**
 * What to actually send as the check's target.
 *
 * For `http` the backend requires a scheme (`checkhttp.Validate`: "must start
 * with http:// or https://"), so a scheme-less value is promoted to `https://`
 * rather than rejected — typing `example.com` and pressing the button is the
 * fastest path to a first check, and https is the right default guess.
 */
export function normalizeTarget(type: QuickCheckType, raw: string): string {
  const value = raw.trim();
  if (type !== "http" || !value) return value;
  if (/^[a-zA-Z][a-zA-Z0-9+.-]*:\/\//.test(value)) return value;
  return `https://${value}`;
}

/**
 * Validates what the user typed for the selected type.
 *
 * Returns `null` when the value is submittable, otherwise the kind of problem
 * so the caller can pick the localized message. This deliberately replaces the
 * `required` / `type="url"` native constraint validation the input used to
 * carry: a native validation bubble is unlocalized, is not announced to
 * assistive tech, and silently blocks the submit handler from ever running.
 */
export function validateTarget(
  type: QuickCheckType,
  raw: string,
): QuickTargetProblem | null {
  const value = raw.trim();
  if (!value) return "empty";
  if (!targetAppliesTo(type, value)) return "invalid";

  if (type === "http") {
    let parsed: URL;
    try {
      parsed = new URL(normalizeTarget(type, value));
    } catch {
      return "invalid";
    }
    if (!parsed.hostname) return "invalid";
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
      return "invalid";
    }
    return null;
  }

  // Host-based types: `targetAppliesTo` already ruled out schemes, paths and
  // whitespace. A lone dot or a leading/trailing dot is still nonsense.
  if (value === "." || value.startsWith(".") || value.endsWith(".")) {
    return "invalid";
  }
  return null;
}
