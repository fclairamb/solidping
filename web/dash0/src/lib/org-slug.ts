/**
 * Org-slug normalization, kept byte-for-byte in step with the server's
 * `orgslug.Slugify` (`server/internal/orgslug/orgslug.go`).
 *
 * This matters more than it looks: /no-org now submits an org with NO slug and
 * lets the server derive one from the name, while the form still shows the user
 * a "will be reachable as …" preview. If the two normalizers disagree — as they
 * did while this ran on a generic `[^a-z0-9]+ -> "-"` regex, which turns
 * "Alice's org" into `alice-s-org` where the server produces `alices-org` — the
 * preview quietly lies about the address the user is about to get.
 *
 * The pipeline, in the server's exact order: lowercase, spaces to `-`, DROP
 * every other non-`[a-z0-9-]` character (do not replace it with a hyphen),
 * collapse repeated `-`, trim `-`, refuse anything shorter than MIN_LEN, cap at
 * MAX_LEN, then trim a trailing `-` the cap may have introduced.
 */

/** Minimum length of a valid org slug (orgslug.MinLen). */
export const ORG_SLUG_MIN_LEN = 3;
/** Maximum length of a valid org slug, suffix included (orgslug.MaxLen). */
export const ORG_SLUG_MAX_LEN = 20;

/**
 * Normalizes one candidate to a valid org-slug base, or `""` when nothing
 * usable remains — matching `orgslug.Slugify`, which returns `""` for a
 * candidate that normalizes to fewer than 3 characters.
 */
export function orgSlugify(name: string): string {
  let base = name.toLowerCase().replaceAll(" ", "-");

  base = Array.from(base)
    .filter((ch) => (ch >= "a" && ch <= "z") || (ch >= "0" && ch <= "9") || ch === "-")
    .join("");

  while (base.includes("--")) {
    base = base.replaceAll("--", "-");
  }

  base = trimHyphens(base);

  if (base.length < ORG_SLUG_MIN_LEN) {
    return "";
  }

  if (base.length > ORG_SLUG_MAX_LEN) {
    base = base.slice(0, ORG_SLUG_MAX_LEN);
  }

  // Capping can leave a trailing hyphen; the result is still >= MIN_LEN because
  // we only cap above MAX_LEN and trim at most one hyphen.
  return base.replace(/-+$/, "");
}

function trimHyphens(value: string): string {
  return value.replace(/^-+/, "").replace(/-+$/, "");
}
