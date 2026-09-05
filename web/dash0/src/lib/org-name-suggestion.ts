/**
 * The organization name /no-org proposes to an account that no org adopted.
 *
 * A brand-new user who just confirmed their registration has no company, no URL
 * slug and no reason to invent either before they can see a single check. The
 * page therefore ships the create form pre-filled with a proposal: "Alice's
 * organization" when we know a first name, a friendly two-word name otherwise.
 * It is a DEFAULT, never a lock — the field stays editable and the Advanced
 * slug toggle keeps working.
 *
 * This module is deliberately i18n-free: it returns a discriminated result and
 * lets the caller render the possessive form from the locale bundle, because
 * "Alice's organization" / "L'organisation d'Alice" / "Organisation von Alice"
 * are not the same sentence with a name glued on.
 */

/**
 * What /no-org should propose. `personal` carries the first name for the
 * caller's i18n interpolation; `random` carries a ready-to-use display name
 * (the word list is English-neutral by design — see WORD_LIST_NOTE below).
 */
export type OrgNameSuggestion =
  | { kind: "personal"; firstName: string }
  | { kind: "random"; name: string };

/*
 * WORD_LIST_NOTE — the fallback vocabulary.
 *
 * Short, neutral and brand-safe on purpose: no real company or person names, no
 * adjectives that read as a judgement of the user, nothing that turns awkward
 * once it is also the org's URL. Every entry must slugify to something a person
 * would be happy to see in an address bar.
 */
const ADJECTIVES = [
  "Bright",
  "Calm",
  "Clever",
  "Golden",
  "Happy",
  "Lucky",
  "Quiet",
  "Rapid",
  "Silver",
  "Sunny",
  "Swift",
  "Vivid",
] as const;

const NOUNS = [
  "Anchor",
  "Beacon",
  "Comet",
  "Falcon",
  "Harbor",
  "Lantern",
  "Meadow",
  "Otter",
  "Panda",
  "River",
  "Summit",
  "Willow",
] as const;

/**
 * Extracts the first name from a display name, or `null` when there is nothing
 * usable. Unicode-aware: a non-Latin name ("李雷", "Алиса Иванова") is a
 * perfectly good first name and must not fall through to the random list.
 *
 * `null` is returned for undefined/empty/whitespace input and for a first token
 * that carries no letter or digit at all (a lone "-" or an emoji), since
 * "'s organization" is not a name.
 */
export function firstNameOf(name: string | null | undefined): string | null {
  if (!name) return null;

  const first = name.trim().split(/\s+/)[0] ?? "";
  if (!first) return null;

  // \p{L}\p{N} rather than /[a-z0-9]/i — see the non-Latin note above.
  if (!/[\p{L}\p{N}]/u.test(first)) return null;

  return first;
}

/**
 * A friendly two-word name from the built-in word list.
 *
 * Deterministic for a given numeric `seed` so tests (and a re-render) are
 * stable; called without one it seeds itself from `Math.random()`.
 */
export function randomOrgName(seed?: number): string {
  const base =
    seed === undefined || !Number.isFinite(seed)
      ? Math.floor(Math.random() * ADJECTIVES.length * NOUNS.length)
      : Math.floor(Math.abs(seed));

  const adjective = ADJECTIVES[base % ADJECTIVES.length];
  const noun = NOUNS[Math.floor(base / ADJECTIVES.length) % NOUNS.length];

  return `${adjective} ${noun}`;
}

/**
 * The proposal for a given account: their first name when we have one, a random
 * friendly name otherwise.
 *
 * The email local part is deliberately NOT a middle fallback — turning
 * `alice.smith@acme.com` into a personal-looking proposal guesses at an
 * identity the user never typed, and reads badly for role addresses
 * (`ops@`, `noreply@`).
 */
export function suggestOrgName(
  userName: string | null | undefined,
  seed?: number,
): OrgNameSuggestion {
  const firstName = firstNameOf(userName);

  if (firstName) {
    return { kind: "personal", firstName };
  }

  return { kind: "random", name: randomOrgName(seed) };
}
