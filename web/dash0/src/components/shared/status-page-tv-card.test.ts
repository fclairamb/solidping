import { describe, expect, it } from "vitest";

import statusPagesEn from "@/locales/en/statusPages.json";
import statusPagesFr from "@/locales/fr/statusPages.json";
import statusPagesDe from "@/locales/de/statusPages.json";
import statusPagesEs from "@/locales/es/statusPages.json";

/**
 * Locale parity for the TV-mode card (spec 2026-08-29-08).
 *
 * The card is where an operator learns that the kiosk token is shown once and
 * cannot be retrieved. A missing key there renders the raw key string in place
 * of that warning, and the operator discovers the omission when they are
 * standing in front of a blank television with no way to recover the token.
 */

type TvMode = Record<string, string>;

const LOCALES: Record<string, TvMode> = {
  fr: (statusPagesFr as Record<string, unknown>).tvMode as TvMode,
  de: (statusPagesDe as Record<string, unknown>).tvMode as TvMode,
  es: (statusPagesEs as Record<string, unknown>).tvMode as TvMode,
};

const en = (statusPagesEn as Record<string, unknown>).tvMode as TvMode;

// Exactly the keys status-page-tv-card.tsx asks i18next for.
const RENDERED_KEYS = [
  "title",
  "description",
  "publicNote",
  "restrictedNote",
  "generate",
  "regenerate",
  "revoke",
  "tokenLabel",
  "tokenShownOnce",
  "tokenGenerated",
  "tokenRevoked",
  "tokenFailed",
  "regenerateTitle",
  "regenerateDescription",
  "revokeTitle",
  "revokeDescription",
];

describe("TV mode card locales", () => {
  it("English defines every key the card renders", () => {
    expect(RENDERED_KEYS.filter((key) => !en[key])).toEqual([]);
  });

  for (const [name, bundle] of Object.entries(LOCALES)) {
    it(`${name} defines every key English does, and no extras`, () => {
      expect(Object.keys(en).filter((key) => !bundle?.[key])).toEqual([]);
      expect(Object.keys(bundle ?? {}).filter((key) => !en[key])).toEqual([]);
    });

    it(`${name} is translated rather than copied from English`, () => {
      const copied = Object.keys(en).filter((key) => bundle[key] === en[key]);

      expect(copied).toEqual([]);
    });
  }
});
