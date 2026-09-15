// Suggests a pre-fill target for the zero-check dashboard hero's quick-create
// form (components/dashboard/empty-state-onboarding.tsx), based on the signed
// -in user's own email domain.
//
// Spec 2026-09-16-01: someone signing up as `alice@acme.com` almost certainly
// wants to watch `acme.com`. Free-webmail signups get nothing useful from
// their domain, so they get no suggestion and see today's empty field.

import { validateTarget } from "./quick-check-target";

/**
 * Free webmail domains where a signup's own email domain is not a useful
 * monitoring target. Mirrors `freeMailDomains` in
 * server/internal/handlers/auth/autojoin_regex.go verbatim — that file is the
 * source of truth. Duplicating the list was chosen over an API change: this
 * is a UX heuristic where a miss costs the user one select-all, not a
 * security decision, so drift between the two lists is harmless.
 */
const FREE_MAIL_DOMAINS = new Set([
  "gmail.com",
  "googlemail.com",
  "yahoo.com",
  "outlook.com",
  "hotmail.com",
  "live.com",
  "msn.com",
  "icloud.com",
  "me.com",
  "protonmail.com",
  "proton.me",
  "aol.com",
  "gmx.com",
  "mail.com",
  "yandex.com",
  "tutanota.com",
  "zoho.com",
  "qq.com",
  "163.com",
  "126.com",
]);

/**
 * The target to pre-fill the hero with for this user, or `null` for none.
 *
 * Returns the bare email domain (`acme.com`), never `https://`-prefixed: a
 * bare hostname is valid for all three quick-create chips and survives a
 * chip switch (`carryOverTarget` clears an `https://…` URL when moving to
 * Ping / SSL), and `normalizeTarget` already promotes it to `https://` on an
 * HTTP submit.
 *
 * Uses the exact email domain — `alice@mail.acme.com` suggests
 * `mail.acme.com`. Guessing the registrable domain would need a
 * public-suffix list and is not worth it for a value the user can overwrite
 * in one keystroke.
 */
export function suggestQuickTarget(email: string | null | undefined): string | null {
  if (!email) return null;

  const trimmedEmail = email.trim();
  if (!trimmedEmail || !trimmedEmail.includes("@")) return null;

  // The LAST "@": a quoted local part carrying its own "@" (`"a@b"@acme.com`)
  // still resolves to the right domain this way, with no special-casing.
  const at = trimmedEmail.lastIndexOf("@");
  const domain = trimmedEmail.slice(at + 1).trim().toLowerCase();
  if (!domain) return null;

  if (FREE_MAIL_DOMAINS.has(domain)) return null;

  // Reuse the icmp host-shape check rather than writing a second parser: it
  // rejects a domain containing "/", ":", whitespace, or a leading/trailing
  // dot. A bare IP literal passes through unchanged.
  if (validateTarget("icmp", domain) !== null) return null;

  return domain;
}
