import { describe, test, expect } from "bun:test";
import en from "../locales/en/status.json";
import fr from "../locales/fr/status.json";
import de from "../locales/de/status.json";
import es from "../locales/es/status.json";

/**
 * Locale parity for TV mode (spec 2026-08-29-08).
 *
 * A missing key on a wallboard is not a cosmetic bug: i18next falls back to
 * rendering the raw key, so a French office would find `tv.ongoingFor` in
 * 40-pixel type across the middle of its incident board. The board also has no
 * scrollbar and no one to click "reload" — whatever renders is what the room
 * reads for the rest of the day.
 */

const LOCALES = { fr, de, es } as const;

function flatten(value: unknown, prefix = ""): Map<string, string> {
  const out = new Map<string, string>();

  if (typeof value === "string") {
    out.set(prefix, value);

    return out;
  }

  if (value && typeof value === "object") {
    for (const [key, child] of Object.entries(value)) {
      for (const [k, v] of flatten(child, prefix ? `${prefix}.${key}` : key)) {
        out.set(k, v);
      }
    }
  }

  return out;
}

const enTv = flatten((en as Record<string, unknown>).tv, "tv");

describe("TV mode locale parity", () => {
  test("English defines the keys the board actually renders", () => {
    for (const key of [
      "tv.stale",
      "tv.staleSince",
      "tv.updatedAt",
      "tv.ongoingFor",
      "tv.resolvedAgo",
      "tv.resolvedAgoFor",
      "tv.durationMinutes",
      "tv.durationHoursMinutes",
      "tv.durationDaysHours",
      "tv.noIncidentsRecorded",
      "tv.cycling",
      "tv.affectedTitle",
      "tv.affectedCycling",
      "tv.failingFor",
      "tv.failingSectionFor",
      "tv.lockedTitle",
      "tv.lockedDescription",
      "tv.pickAPageTitle",
      "tv.pickAPageDescription",
      "tv.uptimeWindow.24h",
      "tv.uptimeWindow.7d",
      "tv.uptimeWindow.30d",
      "tv.uptimeWindow.90d",
    ]) {
      expect(enTv.has(key)).toBe(true);
    }

    // Plural forms are how the day counter renders; i18next resolves the
    // suffix itself, so both variants have to exist.
    expect(enTv.has("tv.daysSinceLastIncident_one")).toBe(true);
    expect(enTv.has("tv.daysSinceLastIncident_other")).toBe(true);

    // The headline's cause line (spec 2026-09-02-05). Same plural mechanics,
    // and the key it would render raw is 20 characters wide on a wallboard.
    expect(enTv.has("tv.incidentDriven_one")).toBe(true);
    expect(enTv.has("tv.incidentDriven_other")).toBe(true);
    expect(enTv.has("tv.incidentDrivenImpaired_one")).toBe(true);
    expect(enTv.has("tv.incidentDrivenImpaired_other")).toBe(true);
  });

  for (const [name, locale] of Object.entries(LOCALES)) {
    test(`${name} defines every key English does`, () => {
      const theirs = flatten((locale as Record<string, unknown>).tv, "tv");

      expect([...enTv.keys()].filter((key) => !theirs.has(key))).toEqual([]);
      expect([...theirs.keys()].filter((key) => !enTv.has(key))).toEqual([]);
    });

    test(`${name} keeps every interpolation placeholder`, () => {
      const theirs = flatten((locale as Record<string, unknown>).tv, "tv");

      for (const [key, english] of enTv) {
        const wanted = [...english.matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]);
        const got = theirs.get(key) ?? "";

        for (const placeholder of wanted) {
          // A translation that dropped {{duration}} renders "ongoing for" and
          // nothing else — grammatical, plausible, and completely useless.
          expect(got).toContain(`{{${placeholder}}}`);
        }
      }
    });

    test(`${name} is actually translated, not copied from English`, () => {
      const theirs = flatten((locale as Record<string, unknown>).tv, "tv");
      const identical = [...enTv.entries()].filter(
        ([key, english]) => theirs.get(key) === english,
      );

      // Duration units are legitimately near-identical across these locales
      // ("2d 3h"), so only the prose is required to differ.
      const prose = identical.filter(([key]) => !key.startsWith("tv.duration"));

      expect(prose).toEqual([]);
    });
  }
});
