import { describe, expect, it } from "vitest";

import type { StatusPage } from "@/api/hooks";
import {
  findCheckPublications,
  selectorMatchesCheck,
} from "./check-publication";

/**
 * Spec 2026-09-16-11. The value of this module is entirely in the NEGATIVE
 * cases: a check that is already visible must not be offered a duplicate row,
 * and the three ways it can already be visible do not look alike in the data.
 * A wrong answer here is either a check published twice (once inside a group
 * roll-up, once beside it) or a check the operator is told is published when it
 * is not.
 */

const page = (sections: StatusPage["sections"]): StatusPage =>
  ({
    uid: "page-1",
    name: "Public",
    slug: "public",
    sections,
  }) as StatusPage;

describe("selectorMatchesCheck", () => {
  it("matches everything under an all rule", () => {
    expect(selectorMatchesCheck({ all: true }, { labels: {} })).toBe(true);
  });

  it("requires every label, with exact values", () => {
    const selector = { labels: { public: "true", tier: "1" } };
    expect(
      selectorMatchesCheck(selector, { labels: { public: "true", tier: "1" } }),
    ).toBe(true);
    // One label missing is not a match — the server ANDs them.
    expect(selectorMatchesCheck(selector, { labels: { public: "true" } })).toBe(
      false,
    );
    // Values are exact, never prefix or truthy.
    expect(
      selectorMatchesCheck(selector, { labels: { public: "TRUE", tier: "1" } }),
    ).toBe(false);
  });

  it("never matches on an absent or empty rule", () => {
    expect(selectorMatchesCheck(undefined, { labels: { a: "b" } })).toBe(false);
    expect(selectorMatchesCheck(null, { labels: { a: "b" } })).toBe(false);
    expect(selectorMatchesCheck({ labels: {} }, { labels: { a: "b" } })).toBe(
      false,
    );
  });
});

describe("findCheckPublications", () => {
  const check = { uid: "chk-1", checkGroupUid: "grp-1", labels: { env: "prod" } };

  it("reports nothing for an unpublished check", () => {
    const pages = [
      page([
        {
          uid: "sec-1",
          name: "Core",
          slug: "core",
          position: 0,
          resources: [{ uid: "r1", checkUid: "chk-other", position: 0 }],
        },
      ]),
    ];
    expect(findCheckPublications(check, pages)).toEqual([]);
  });

  it("reports a hand-placed row as direct", () => {
    const pages = [
      page([
        {
          uid: "sec-1",
          name: "Core",
          slug: "core",
          position: 0,
          resources: [{ uid: "r1", checkUid: "chk-1", position: 0 }],
        },
      ]),
    ];
    expect(findCheckPublications(check, pages)).toEqual([
      {
        pageUid: "page-1",
        pageName: "Public",
        sectionUid: "sec-1",
        sectionName: "Core",
        via: "direct",
      },
    ]);
  });

  it("reports a selector-owned row as selector, not as a manual placement", () => {
    const pages = [
      page([
        {
          uid: "sec-1",
          name: "Auto",
          slug: "auto",
          position: 0,
          selector: { all: true },
          resources: [
            {
              uid: "r1",
              checkUid: "chk-1",
              position: 0,
              managedBySelector: true,
            },
          ],
        },
      ]),
    ];
    expect(findCheckPublications(check, pages)[0].via).toBe("selector");
  });

  it("reports the check's GROUP component as a publication of the check", () => {
    // The row targets the group, never the check — a naive checkUid scan finds
    // nothing here and offers a duplicate row beside the roll-up.
    const pages = [
      page([
        {
          uid: "sec-1",
          name: "Platform",
          slug: "platform",
          position: 0,
          resources: [{ uid: "r1", checkGroupUid: "grp-1", position: 0 }],
        },
      ]),
    ];
    expect(findCheckPublications(check, pages)).toEqual([
      {
        pageUid: "page-1",
        pageName: "Public",
        sectionUid: "sec-1",
        sectionName: "Platform",
        via: "group",
      },
    ]);
  });

  it("ignores a group row for a check in a different group", () => {
    const pages = [
      page([
        {
          uid: "sec-1",
          name: "Platform",
          slug: "platform",
          position: 0,
          resources: [{ uid: "r1", checkGroupUid: "grp-2", position: 0 }],
        },
      ]),
    ];
    expect(findCheckPublications(check, pages)).toEqual([]);
  });

  it("ignores a group row when the check belongs to no group", () => {
    const pages = [
      page([
        {
          uid: "sec-1",
          name: "Platform",
          slug: "platform",
          position: 0,
          resources: [{ uid: "r1", checkGroupUid: "grp-1", position: 0 }],
        },
      ]),
    ];
    expect(
      findCheckPublications({ uid: "chk-1", labels: {} }, pages),
    ).toEqual([]);
  });

  it("prefers the direct row over a group row in the same section", () => {
    const pages = [
      page([
        {
          uid: "sec-1",
          name: "Platform",
          slug: "platform",
          position: 0,
          resources: [
            { uid: "r1", checkGroupUid: "grp-1", position: 0 },
            { uid: "r2", checkUid: "chk-1", position: 1 },
          ],
        },
      ]),
    ];
    expect(findCheckPublications(check, pages)[0].via).toBe("direct");
  });

  it("reports a matching rule that has not been reconciled yet", () => {
    // The window between creating a check and the next reconcile is exactly
    // when someone asks "why isn't it on the page?" — answering "it is not
    // published" there would be wrong.
    const pages = [
      page([
        {
          uid: "sec-1",
          name: "Prod",
          slug: "prod",
          position: 0,
          selector: { labels: { env: "prod" } },
          resources: [],
        },
      ]),
    ];
    expect(findCheckPublications(check, pages)[0].via).toBe("selector");
  });

  it("returns one entry per section, across pages", () => {
    const pages = [
      page([
        {
          uid: "sec-1",
          name: "Core",
          slug: "core",
          position: 0,
          resources: [{ uid: "r1", checkUid: "chk-1", position: 0 }],
        },
        {
          uid: "sec-2",
          name: "Other",
          slug: "other",
          position: 1,
          resources: [],
        },
      ]),
      {
        uid: "page-2",
        name: "Internal",
        slug: "internal",
        sections: [
          {
            uid: "sec-3",
            name: "Everything",
            slug: "everything",
            position: 0,
            selector: { all: true },
            resources: [],
          },
        ],
      } as StatusPage,
    ];
    expect(findCheckPublications(check, pages).map((p) => p.sectionUid)).toEqual(
      ["sec-1", "sec-3"],
    );
  });
});
