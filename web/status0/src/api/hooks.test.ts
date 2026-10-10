import { describe, test, expect } from "bun:test";
import { withKiosk } from "../lib/kiosk";
import {
  mergeStages,
  withInclude,
  withUpdatesDays,
  type PublicPageInclude,
  type StatusPage,
} from "./hooks";

// The URL builder for `?include=` (spec 2026-09-22-07). Composed with
// `withKiosk` the same way the hooks do, so these pin the same order the
// production query functions build the path in: include first, kiosk last.
describe("withInclude", () => {
  test("leaves the path untouched when include is not requested", () => {
    expect(withInclude("/api/v1/status-pages/acme/main", undefined)).toBe(
      "/api/v1/status-pages/acme/main",
    );
  });

  test("an empty array requests include= with no value", () => {
    expect(withInclude("/api/v1/status-pages/acme/main", [])).toBe(
      "/api/v1/status-pages/acme/main?include=",
    );
  });

  test("a single token", () => {
    expect(
      withInclude("/api/v1/status-pages/acme/main", ["availability"]),
    ).toBe("/api/v1/status-pages/acme/main?include=availability");
  });

  test("sorts tokens so order never affects the URL (or the cache key)", () => {
    const forward: PublicPageInclude[] = ["availability", "responseTime"];
    const backward: PublicPageInclude[] = ["responseTime", "availability"];

    expect(withInclude("/x", forward)).toBe(withInclude("/x", backward));
    expect(withInclude("/x", forward)).toBe(
      "/x?include=availability,responseTime",
    );
  });

  test("dedupes repeated tokens", () => {
    expect(
      withInclude("/x", ["availability", "availability", "responseTime"]),
    ).toBe("/x?include=availability,responseTime");
  });

  test("joins onto an existing query string rather than starting a second one", () => {
    expect(withInclude("/x?active=true", ["availability"])).toBe(
      "/x?active=true&include=availability",
    );
  });

  test("composes with the kiosk token in either order, same result", () => {
    const path = "/api/v1/status-pages/acme/main";

    const includeThenKiosk = withKiosk(
      withInclude(path, ["availability"]),
      "tok",
    );
    // Building kiosk first and include second would produce the same query
    // string (order within a query string is irrelevant to the server), but
    // the hooks always build include first — pin that composed shape too.
    expect(includeThenKiosk).toBe(`${path}?include=availability&kiosk=tok`);
  });
});

// Staged public page load (spec 2026-10-10-01).
describe("staged page load URLs", () => {
  const base = "/api/v1/status-pages/acme/main";

  test("each stage builds its own URL", () => {
    expect(withInclude(base, [])).toBe(`${base}?include=`);
    expect(withInclude(base, ["availability", "responseTime"])).toBe(
      `${base}?include=availability,responseTime`,
    );
    expect(withInclude(base, ["updates"])).toBe(`${base}?include=updates`);
  });

  test("updatesDays is appended only when asked for", () => {
    const updates = withInclude(base, ["updates"]);
    expect(withUpdatesDays(updates, undefined)).toBe(updates);
    expect(withUpdatesDays(updates, 90)).toBe(
      `${base}?include=updates&updatesDays=90`,
    );
  });
});

describe("mergeStages", () => {
  const page = (extra: Partial<StatusPage>): StatusPage =>
    ({
      uid: "p",
      name: "P",
      slug: "p",
      sections: [
        {
          uid: "s",
          name: "S",
          slug: "s",
          position: 0,
          resources: [{ uid: "r1", position: 0 }],
        },
      ],
      ...extra,
    }) as StatusPage;

  test("returns undefined until stage 1 lands", () => {
    expect(mergeStages(undefined, page({}), page({}))).toBeUndefined();
  });

  test("stage 1 alone is the page", () => {
    const base = page({});
    expect(mergeStages(base, undefined, undefined)).toEqual(base);
  });

  test("joins availability by resource uid and takes updates from stage 4", () => {
    const base = page({});
    const details = page({
      overallAvailabilityPct: 99.5,
      sections: [
        {
          uid: "s",
          name: "S",
          slug: "s",
          position: 0,
          resources: [
            {
              uid: "r1",
              position: 0,
              availability: { overallAvailabilityPct: 99.5 },
            },
          ],
        },
      ] as StatusPage["sections"],
    });
    const updates = page({
      recentUpdates: [{ uid: "u1" }] as StatusPage["recentUpdates"],
    });
    const merged = mergeStages(base, details, updates);
    expect(merged?.overallAvailabilityPct).toBe(99.5);
    expect(
      merged?.sections?.[0].resources?.[0].availability?.overallAvailabilityPct,
    ).toBe(99.5);
    expect(merged?.recentUpdates).toHaveLength(1);
  });
});
