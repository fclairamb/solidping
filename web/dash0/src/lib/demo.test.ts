import { describe, expect, it } from "vitest";

import demoAuthDe from "@/locales/de/auth.json";
import demoAuthEn from "@/locales/en/auth.json";
import demoAuthEs from "@/locales/es/auth.json";
import demoAuthFr from "@/locales/fr/auth.json";
import demoOrgDe from "@/locales/de/org.json";
import demoOrgEn from "@/locales/en/org.json";
import demoOrgEs from "@/locales/es/org.json";
import demoOrgFr from "@/locales/fr/org.json";

import {
  DEMO_ALLOWED_CHECK_TYPES,
  DEMO_MIN_PERIOD_SECONDS,
  canDemoEditCheck,
  demoFlagFromLocation,
  filterCheckTypesForDemo,
  isDemoReadOnlyError,
  parseDemoFlag,
} from "./demo";

// Spec 2026-09-06-02. None of the rules below are security controls — the
// server refuses every one of these writes on its own — so what these tests
// pin is that the UI does not OFFER an action whose only outcome would be a
// refusal, and, just as importantly, that it does not withhold anything from
// an ordinary customer.

describe("canDemoEditCheck", () => {
  it("never restricts an ordinary session", () => {
    // The most important case in the file: the ownership rule is a property of
    // the demo, not of the product. A colleague must still be able to edit a
    // check somebody else on the team created.
    expect(canDemoEditCheck(false, "alice", "bob")).toBe(true);
    expect(canDemoEditCheck(false, "alice", null)).toBe(true);
    expect(canDemoEditCheck(undefined, undefined, undefined)).toBe(true);
  });

  it("lets a demo visitor edit only what they created", () => {
    expect(canDemoEditCheck(true, "visitor", "visitor")).toBe(true);
    expect(canDemoEditCheck(true, "visitor", "someone-else")).toBe(false);
  });

  it("treats a check with no creator as uneditable in the demo", () => {
    // The seeded catalogue. created_by is NULL there, which is exactly what
    // makes it immutable server-side, with no "protected" flag anywhere.
    expect(canDemoEditCheck(true, "visitor", null)).toBe(false);
    expect(canDemoEditCheck(true, "visitor", undefined)).toBe(false);
  });

  it("fails closed when the current user is unknown", () => {
    expect(canDemoEditCheck(true, undefined, "visitor")).toBe(false);
  });
});

describe("filterCheckTypesForDemo", () => {
  const types = [
    { type: "http" },
    { type: "ssl" },
    { type: "smtp" },
    { type: "browser" },
    { type: "postgresql" },
  ];

  it("leaves an ordinary session's picker untouched", () => {
    expect(filterCheckTypesForDemo(false, types)).toHaveLength(types.length);
  });

  it("narrows a demo session to the side-effect-free probes", () => {
    expect(filterCheckTypesForDemo(true, types).map((e) => e.type)).toEqual([
      "http",
      "ssl",
    ]);
  });

  it("excludes the abuse-prone types the spec names", () => {
    const allowed = new Set<string>(DEMO_ALLOWED_CHECK_TYPES);
    for (const excluded of ["smtp", "email", "browser", "ssh", "kubernetes", "docker"]) {
      expect(allowed.has(excluded)).toBe(false);
    }
  });
});

describe("demo constants", () => {
  it("mirrors the server's allowlist", () => {
    // Must stay in sync with demoAllowedCheckTypes in
    // server/internal/handlers/checks/demo.go.
    expect([...DEMO_ALLOWED_CHECK_TYPES]).toEqual(["http", "tcp", "icmp", "dns", "ssl"]);
  });

  it("mirrors the server's period floor", () => {
    expect(DEMO_MIN_PERIOD_SECONDS).toBe(60);
  });
});

// Locale parity. A missing key here renders a raw dotted path on the login
// page or in the persistent banner — the two most-seen surfaces of the whole
// feature, and the ones an evaluator sees FIRST.

const AUTH_LOCALES = [
  ["en", demoAuthEn],
  ["fr", demoAuthFr],
  ["de", demoAuthDe],
  ["es", demoAuthEs],
] as const;

const ORG_LOCALES = [
  ["en", demoOrgEn],
  ["fr", demoOrgFr],
  ["de", demoOrgDe],
  ["es", demoOrgEs],
] as const;

const AUTH_DEMO_KEYS = ["tryLiveDemo", "loginHint"] as const;

const ORG_DEMO_KEYS = [
  "title",
  "description",
  "signUp",
  "readOnly",
  "readOnlyHint",
  "checkExpires",
  "seededCheck",
] as const;

describe("demo locale parity", () => {
  it.each(AUTH_LOCALES)("auth.json/%s carries every demo key", (_locale, bundle) => {
    const demo = (bundle as Record<string, unknown>).demo as Record<string, string>;
    expect(demo).toBeDefined();
    for (const key of AUTH_DEMO_KEYS) {
      expect(typeof demo[key]).toBe("string");
      expect(demo[key].trim()).not.toBe("");
    }
  });

  it.each(ORG_LOCALES)("org.json/%s carries every demo key", (_locale, bundle) => {
    const demo = (bundle as Record<string, unknown>).demo as Record<string, string>;
    expect(demo).toBeDefined();
    for (const key of ORG_DEMO_KEYS) {
      expect(typeof demo[key]).toBe("string");
      expect(demo[key].trim()).not.toBe("");
    }
  });

  it("does not ship an untranslated copy of the English string", () => {
    // A locale file that merely copied English is worse than a missing key:
    // the parity test above would pass and the page would silently be in the
    // wrong language.
    const en = (demoOrgEn as Record<string, unknown>).demo as Record<string, string>;
    for (const [locale, bundle] of ORG_LOCALES) {
      if (locale === "en") continue;
      const demo = (bundle as Record<string, unknown>).demo as Record<string, string>;
      expect(demo.title, `${locale} title is untranslated`).not.toBe(en.title);
      expect(demo.description, `${locale} description is untranslated`).not.toBe(
        en.description,
      );
    }
  });
});

// Spec 2026-09-07-02: the `?demo=` deep link is now parsed on four surfaces
// (`/`, `/login`, `/orgs/$org/login`, and the `/orgs/$org` layout's
// beforeLoad). These tests are what keeps them agreeing on what the flag is.
describe("parseDemoFlag", () => {
  it("accepts every shape the two query parsers produce", () => {
    // TanStack's search parser hands over a native boolean for `?demo=true`
    // and the NUMBER 1 for `?demo=1`; URLSearchParams hands over the strings.
    expect(parseDemoFlag(true)).toBe(true);
    expect(parseDemoFlag("true")).toBe(true);
    expect(parseDemoFlag("1")).toBe(true);
    expect(parseDemoFlag(1)).toBe(true);
  });

  it("returns undefined — never false — for everything else", () => {
    // undefined rather than false so the param drops out of the URL instead
    // of being serialised back as `?demo=false`.
    for (const value of [undefined, null, false, "false", "0", 0, "", "yes", {}, []]) {
      expect(parseDemoFlag(value), `${JSON.stringify(value)}`).toBeUndefined();
    }
  });
});

describe("demoFlagFromLocation", () => {
  it("reads the flag off an already-parsed search object", () => {
    expect(demoFlagFromLocation({ demo: true }, "")).toBe(true);
    expect(demoFlagFromLocation({ demo: 1 }, "")).toBe(true);
  });

  it("falls back to the raw search string, with or without the leading ?", () => {
    // A layout route declares no validateSearch of its own, so what lands in
    // location.search is whatever the default parser made of it. The raw
    // string is the belt to that braces.
    expect(demoFlagFromLocation(undefined, "?demo=true")).toBe(true);
    expect(demoFlagFromLocation({}, "demo=1")).toBe(true);
    expect(demoFlagFromLocation({}, "?foo=bar&demo=true")).toBe(true);
  });

  it("is undefined when neither source carries the flag", () => {
    expect(demoFlagFromLocation({}, "")).toBeUndefined();
    expect(demoFlagFromLocation(undefined, undefined)).toBeUndefined();
    expect(demoFlagFromLocation({ demo: false }, "?demo=false")).toBeUndefined();
    expect(demoFlagFromLocation({ other: true }, "?other=true")).toBeUndefined();
  });
});

describe("isDemoReadOnlyError", () => {
  it("recognises the write guard's refusal", () => {
    expect(isDemoReadOnlyError({ code: "DEMO_READ_ONLY" })).toBe(true);
  });

  it("lets every other failure through", () => {
    // The whole point: a secondary write that fails for a REAL reason must
    // still surface, or an ordinary user silently loses a channel binding.
    expect(isDemoReadOnlyError({ code: "VALIDATION_ERROR" })).toBe(false);
    expect(isDemoReadOnlyError({ code: "FORBIDDEN" })).toBe(false);
    expect(isDemoReadOnlyError(new Error("network"))).toBe(false);
    expect(isDemoReadOnlyError(null)).toBe(false);
    expect(isDemoReadOnlyError(undefined)).toBe(false);
    expect(isDemoReadOnlyError("DEMO_READ_ONLY")).toBe(false);
  });
});
