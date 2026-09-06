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
  filterCheckTypesForDemo,
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
