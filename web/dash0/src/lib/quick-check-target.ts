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
// `icmp.host` / `ssl.host` expect.
const NOT_A_BARE_HOST = /^[a-zA-Z][a-zA-Z0-9+.-]*:\/\/|[/?#]|\s/;

const SCHEME = /^[a-zA-Z][a-zA-Z0-9+.-]*:\/\//;

/**
 * Whether `value` carries a `:port` suffix.
 *
 * This has to tell `example.com:8443` (a port) apart from `2001:db8::1` (an
 * IPv6 literal), because the two differ only in how many colons they have:
 *
 *   - exactly one colon, unbracketed → `host:port`;
 *   - two or more colons, unbracketed → IPv6 literal, which is a perfectly
 *     good ping target and SSL host and must survive untouched;
 *   - bracketed → the same rule one level down: `[2001:db8::1]` is a host,
 *     `[2001:db8::1]:443` carries a port.
 *
 * A port is disqualifying for `icmp` and `ssl` and only for them: ICMP has no
 * concept of a port at all, and the SSL checker takes its port in a separate
 * `port` config field (checkssl/config.go), so `host:port` in the host field is
 * never what the user meant. Neither backend validates the host's shape — they
 * only reject an empty one — so a value like this is accepted, stored, and then
 * fails at runtime, which is exactly the "looks like it worked" outcome this
 * form exists to prevent.
 */
function carriesPort(value: string): boolean {
  if (value.startsWith("[")) {
    const end = value.indexOf("]");
    if (end === -1) return false; // malformed; the shape rules deal with it
    return value.slice(end + 1).startsWith(":");
  }
  return value.split(":").length - 1 === 1;
}

/** Strips trailing slashes: `example.com/` is a hostname somebody pasted from
 * a browser's address bar, not a path — the five characters are recoverable, so
 * the value is trimmed rather than thrown away. */
function stripTrailingSlashes(value: string): string {
  return value.replace(/\/+$/, "");
}

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
  // http takes a full URL, a bare hostname, and a host:port pair alike
  // (normalizeTarget adds the scheme the backend insists on).
  if (type === "http") return true;
  return !NOT_A_BARE_HOST.test(value) && !carriesPort(value);
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
  if (!value) return value;
  // Host-based types: a pasted trailing slash is not a path, just address-bar
  // residue. Strip it instead of refusing the value.
  if (type !== "http") return stripTrailingSlashes(value);
  if (SCHEME.test(value)) return value;
  // A scheme-less value becomes https, port or no port. Guessing the scheme
  // FROM the port (`:80` → http) was considered and rejected: it is a second
  // heuristic that gets `:8080` wrong just as easily, and an https target that
  // is really http fails loudly on the first run rather than silently.
  return `https://${value}`;
}

/**
 * The value to leave in the input when the user picks `type` with `raw`
 * already typed.
 *
 * Returns the value to keep — possibly tidied, never re-typed from scratch — or
 * an empty string when it genuinely cannot apply to the new type. The spec's
 * rule is "clear ONLY when it cannot possibly apply", so a recoverable value
 * (`example.com/`) is trimmed rather than discarded, while `https://example.com`
 * or `example.com:8443` moving to Ping is discarded because neither can be a
 * host.
 */
export function carryOverTarget(type: QuickCheckType, raw: string): string {
  // Everything applies to http, verbatim.
  if (type === "http") return raw;

  const trimmed = raw.trim();
  if (!trimmed) return raw;

  const stripped = stripTrailingSlashes(trimmed);
  if (!targetAppliesTo(type, stripped)) return "";
  // Only rewrite the field when stripping actually changed something; otherwise
  // leave the user's own text (whitespace included) exactly as they typed it.
  return stripped === trimmed ? raw : stripped;
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
  if (!raw.trim()) return "empty";
  // Validate what would actually be sent, so a recoverable trailing slash is
  // not reported as an error the user has to fix by hand.
  const value = normalizeTarget(type, raw);
  if (!value) return "empty";
  if (type !== "http" && !targetAppliesTo(type, value)) return "invalid";

  if (type === "http") {
    let parsed: URL;
    try {
      parsed = new URL(value);
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
