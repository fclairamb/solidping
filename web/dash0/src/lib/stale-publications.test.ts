import { describe, expect, it } from "vitest";
import enStatusPages from "@/locales/en/statusPages.json";
import frStatusPages from "@/locales/fr/statusPages.json";
import deStatusPages from "@/locales/de/statusPages.json";
import esStatusPages from "@/locales/es/statusPages.json";
import type { IncidentPublication, StalePublication } from "@/api/hooks";
import {
  countStale,
  groupStaleByPage,
  isStalePublication,
} from "./stale-publications";

function publication(
  partial: Partial<IncidentPublication> = {},
): IncidentPublication {
  return {
    uid: "pub-1",
    statusPageUid: "page-1",
    incidentUid: "inc-1",
    title: "Some services are experiencing issues",
    state: "identified",
    autoCreated: false,
    humanTouched: false,
    publishedAt: "2026-08-23T12:17:51Z",
    createdAt: "2026-08-23T12:17:51Z",
    updatedAt: "2026-08-23T12:18:21Z",
    stale: true,
    ...partial,
  };
}

function entry(
  pageUid: string,
  pageName: string,
  pub: Partial<IncidentPublication> = {},
): StalePublication {
  return {
    page: { uid: pageUid, name: pageName } as StalePublication["page"],
    publication: publication({ statusPageUid: pageUid, ...pub }),
  };
}

describe("isStalePublication", () => {
  it("flags an open, incident-linked entry the server called stale", () => {
    expect(isStalePublication(publication())).toBe(true);
  });

  it("never flags a resolved entry", () => {
    // Nothing left to close: the public page already agrees with reality.
    expect(isStalePublication(publication({ state: "resolved" }))).toBe(false);
    expect(
      isStalePublication(
        publication({ resolvedAt: "2026-08-23T13:00:00Z" }),
      ),
    ).toBe(false);
  });

  it("never flags a free-form entry", () => {
    // No incident to contradict it — a "we are migrating tonight" notice is
    // its author's to close, and warning about it would be noise forever.
    expect(
      isStalePublication(publication({ incidentUid: undefined })),
    ).toBe(false);
  });

  it("does not invent staleness the server did not report", () => {
    expect(isStalePublication(publication({ stale: false }))).toBe(false);
    expect(isStalePublication(publication({ stale: undefined }))).toBe(false);
  });
});

describe("groupStaleByPage", () => {
  it("returns nothing for a clean org", () => {
    expect(groupStaleByPage([])).toEqual([]);
    expect(countStale(groupStaleByPage([]))).toBe(0);
  });

  it("collapses several entries on one page into a single line", () => {
    const groups = groupStaleByPage([
      entry("page-1", "Public status", { uid: "a" }),
      entry("page-1", "Public status", { uid: "b" }),
    ]);

    expect(groups).toHaveLength(1);
    expect(groups[0].pageName).toBe("Public status");
    expect(groups[0].publications.map((p) => p.uid)).toEqual(["a", "b"]);
    expect(countStale(groups)).toBe(2);
  });

  it("keeps distinct pages apart, in arrival order", () => {
    const groups = groupStaleByPage([
      entry("page-2", "Partners", { uid: "b" }),
      entry("page-1", "Public status", { uid: "a" }),
      entry("page-2", "Partners", { uid: "c" }),
    ]);

    expect(groups.map((g) => g.pageUid)).toEqual(["page-2", "page-1"]);
    expect(groups[0].publications).toHaveLength(2);
    expect(groups[1].publications).toHaveLength(1);
    expect(countStale(groups)).toBe(3);
  });
});

describe("stalePublications locale parity", () => {
  const locales = {
    fr: frStatusPages,
    de: deStatusPages,
    es: esStatusPages,
  } as Record<string, Record<string, unknown>>;

  const english = (enStatusPages as Record<string, unknown>)
    .stalePublications as Record<string, string>;

  it("English defines every key the banner renders", () => {
    for (const key of [
      "title",
      "description_one",
      "description_other",
      "review",
      "hint",
    ]) {
      expect(english[key]).toBeTruthy();
    }
  });

  for (const [name, locale] of Object.entries(locales)) {
    it(`${name} defines the same keys, translated, with the placeholders kept`, () => {
      const theirs = locale.stalePublications as Record<string, string>;

      expect(theirs).toBeTruthy();
      expect(Object.keys(theirs).sort()).toEqual(Object.keys(english).sort());

      for (const [key, value] of Object.entries(english)) {
        // A dropped {{page}} would name no page at all, which is the one fact
        // that makes the warning actionable.
        for (const placeholder of value.matchAll(/\{\{(\w+)\}\}/g)) {
          expect(theirs[key]).toContain(`{{${placeholder[1]}}}`);
        }

        expect(theirs[key]).not.toBe(value);
      }
    });
  }
});
