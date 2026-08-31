import { describe, expect, it } from "vitest";

import statusPagesEn from "@/locales/en/statusPages.json";
import statusPagesFr from "@/locales/fr/statusPages.json";
import statusPagesDe from "@/locales/de/statusPages.json";
import statusPagesEs from "@/locales/es/statusPages.json";

import {
  membershipFromSelector,
  membershipIsComplete,
  selectorFromMembership,
} from "./section-membership";

/**
 * Locale parity for the section membership picker (spec 2026-08-29-11).
 *
 * The strings here are not decoration. `publicWarningAll` is the ONLY thing
 * telling an operator that switching a public page's section to "all checks"
 * will publish every check they create from then on — a scratch check named
 * after an internal hostname included. A missing key renders the raw key
 * string, so the warning silently becomes a line of gibberish precisely where
 * it matters most.
 */

type Membership = Record<string, unknown>;

const membershipOf = (bundle: unknown): Membership =>
  ((bundle as Record<string, Record<string, unknown>>).sections
    .membership as Membership) ?? {};

const en = membershipOf(statusPagesEn);

const LOCALES: Record<string, Membership> = {
  fr: membershipOf(statusPagesFr),
  de: membershipOf(statusPagesDe),
  es: membershipOf(statusPagesEs),
};

// Exactly the keys section-membership.tsx and the section card ask i18next for.
const RENDERED_KEYS = [
  "title",
  "manual",
  "all",
  "labels",
  "labelsField",
  "labelsHint",
  "publicWarningTitle",
  "publicWarningAll",
  "publicWarningLabels",
  "autoBadge",
  "autoTooltip",
  "truncated",
];

const RENDERED_HINT_KEYS = ["manual", "all", "labels"];

// The claimed-elsewhere hint (spec 2026-08-31-01) is rendered by
// SelectorClaimedElsewhereAlert, mounted next to `truncated` on the section
// card — a sibling of this file's own component, not this component itself,
// but the same "every key the picker/card renders" umbrella `truncated`
// already sits under above.
const RENDERED_CLAIMED_ELSEWHERE_KEYS = [
  "full_one",
  "full_other",
  "partial_one",
  "partial_other",
];

/**
 * Keys whose translation is legitimately identical to the English, per locale.
 *
 * The "not a verbatim copy" check exists to catch a bundle someone duplicated
 * and forgot to translate. A handful of words genuinely are the same word, and
 * the honest fix is to name them here rather than to weaken the check — or,
 * worse, to invent a longer synonym that then overflows the control it sits in.
 */
const SAME_IN = new Map<string, string[]>([
  // "auto" is the same word in all four, and the badge is deliberately tiny.
  ["autoBadge", ["fr", "de", "es"]],
  // "Manual" is the Spanish word for manual.
  ["manual", ["es"]],
]);

/** flatten renders a nested bundle as dotted paths, so `hint.all` is compared. */
function flatten(value: unknown, prefix = ""): Record<string, string> {
  const out: Record<string, string> = {};

  for (const [key, entry] of Object.entries(value as Record<string, unknown>)) {
    const path = prefix ? `${prefix}.${key}` : key;

    if (entry && typeof entry === "object") {
      Object.assign(out, flatten(entry, path));
    } else {
      out[path] = String(entry);
    }
  }

  return out;
}

describe("section membership locales", () => {
  it("English defines every key the picker renders", () => {
    expect(RENDERED_KEYS.filter((key) => !en[key])).toEqual([]);

    const hint = en.hint as Record<string, string>;
    expect(RENDERED_HINT_KEYS.filter((key) => !hint?.[key])).toEqual([]);

    const claimedElsewhere = en.claimedElsewhere as Record<string, string>;
    expect(
      RENDERED_CLAIMED_ELSEWHERE_KEYS.filter(
        (key) => !claimedElsewhere?.[key],
      ),
    ).toEqual([]);
  });

  const flatEn = flatten(en);

  for (const [name, bundle] of Object.entries(LOCALES)) {
    const flat = flatten(bundle);

    it(`${name} defines every key English does, and no extras`, () => {
      expect(Object.keys(flatEn).filter((key) => !flat[key])).toEqual([]);
      expect(Object.keys(flat).filter((key) => !flatEn[key])).toEqual([]);
    });

    it(`${name} is translated rather than copied from English`, () => {
      const copied = Object.keys(flatEn).filter(
        (key) =>
          !(SAME_IN.get(key) ?? []).includes(name) && flat[key] === flatEn[key],
      );

      expect(copied).toEqual([]);
    });

    it(`${name} keeps the interpolation placeholders`, () => {
      expect(flat.truncated).toContain("{{shown}}");
      expect(flat.truncated).toContain("{{total}}");

      expect(flat["claimedElsewhere.full_one"]).toContain("{{section}}");
      expect(flat["claimedElsewhere.full_other"]).toContain("{{count}}");
      expect(flat["claimedElsewhere.full_other"]).toContain("{{section}}");
      expect(flat["claimedElsewhere.partial_one"]).toContain("{{count}}");
      expect(flat["claimedElsewhere.partial_other"]).toContain("{{count}}");
    });
  }
});

/**
 * Round-tripping the selector.
 *
 * The `manual` → `null` mapping is the load-bearing one: on an update, null is
 * what CLEARS a section's rule. If it returned undefined, the key would be
 * omitted, the API would leave the rule in place, and the operator would watch
 * "switch back to manual" silently do nothing.
 */
describe("membership selector round trip", () => {
  it("reads an absent selector as manual — auto-inclusion is never a default", () => {
    expect(membershipFromSelector(undefined)).toEqual({
      mode: "manual",
      labels: {},
    });
    expect(membershipFromSelector(null)).toEqual({
      mode: "manual",
      labels: {},
    });
    expect(membershipFromSelector({})).toEqual({ mode: "manual", labels: {} });
    expect(membershipFromSelector({ labels: {} })).toEqual({
      mode: "manual",
      labels: {},
    });
  });

  it("round-trips all and labels", () => {
    expect(membershipFromSelector({ all: true })).toEqual({
      mode: "all",
      labels: {},
    });
    expect(membershipFromSelector({ labels: { env: "prod" } })).toEqual({
      mode: "labels",
      labels: { env: "prod" },
    });

    expect(selectorFromMembership({ mode: "all", labels: {} })).toEqual({
      all: true,
    });
    expect(
      selectorFromMembership({ mode: "labels", labels: { env: "prod" } }),
    ).toEqual({ labels: { env: "prod" } });
  });

  it("renders manual as null so an update clears the rule", () => {
    expect(selectorFromMembership({ mode: "manual", labels: {} })).toBeNull();
  });

  it("treats an empty label set as incomplete rather than as everything", () => {
    expect(membershipIsComplete({ mode: "labels", labels: {} })).toBe(false);
    expect(
      membershipIsComplete({ mode: "labels", labels: { env: "prod" } }),
    ).toBe(true);
    expect(membershipIsComplete({ mode: "all", labels: {} })).toBe(true);

    // And it never degrades into an accidental "match everything".
    expect(selectorFromMembership({ mode: "labels", labels: {} })).toBeNull();
  });
});
