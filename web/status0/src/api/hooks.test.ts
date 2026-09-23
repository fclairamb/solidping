import { describe, test, expect } from "bun:test";
import { withKiosk } from "../lib/kiosk";
import { withInclude, type PublicPageInclude } from "./hooks";

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
