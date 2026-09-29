import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

import {
  ALL_EVENT_TYPES,
  EVENT_TYPE_FAMILIES,
  eventFamily,
  isEventType,
} from "@/lib/event-types";
import eventsDe from "@/locales/de/events.json";
import eventsEn from "@/locales/en/events.json";
import eventsEs from "@/locales/es/events.json";
import eventsFr from "@/locales/fr/events.json";

// EVENT_GO_PATH resolves server/internal/db/models/event.go from THIS test
// file's own location (web/dash0/src/lib → four levels below the repo root),
// so it works both from the repo root and from web/dash0. Deliberately the
// real Go source rather than a copy of the list: a copy would go stale
// silently, which is exactly the drift this suite exists to catch.
const EVENT_GO_PATH = join(
  import.meta.dirname,
  "../../../..",
  "server/internal/db/models/event.go",
);

/** Every `EventTypeXxx EventType = "..."` constant value in event.go. */
function parseBackendEventTypes(): string[] {
  const source = readFileSync(EVENT_GO_PATH, "utf-8");
  const matches = source.matchAll(/EventType\w+\s+EventType\s*=\s*"([^"]+)"/g);
  return [...matches].map((m) => m[1]);
}

const BACKEND_EVENT_TYPES = parseBackendEventTypes();
const ALL_TYPES: readonly string[] = ALL_EVENT_TYPES;
const ALL_FAMILIES: readonly string[] = EVENT_TYPE_FAMILIES;

describe("parseBackendEventTypes (positive control)", () => {
  // Guards against the regex silently matching nothing — a vacuously-passing
  // guard is worse than no guard.
  it("actually found constants in event.go", () => {
    expect(BACKEND_EVENT_TYPES.length).toBeGreaterThan(15);
  });
});

describe("ALL_EVENT_TYPES mirrors the server's EventType* constants", () => {
  it("has no type the server cannot write", () => {
    const extra = ALL_TYPES.filter((type) => !BACKEND_EVENT_TYPES.includes(type));
    expect(
      extra,
      `event.go defines no constant for ${extra.join(", ")}: drop it from ` +
        `lib/event-types.ts, or add the constant to ` +
        `server/internal/db/models/event.go.`,
    ).toEqual([]);
  });

  it("has every type the server can write", () => {
    const missing = BACKEND_EVENT_TYPES.filter((type) => !ALL_TYPES.includes(type));
    expect(
      missing,
      `event.go defines ${missing.join(", ")} with no entry in ` +
        `lib/event-types.ts: add it under its family, so the events page's ` +
        `type filter can offer it.`,
    ).toEqual([]);
  });

  it("lists each type exactly once", () => {
    expect(new Set(ALL_TYPES).size).toBe(ALL_TYPES.length);
  });
});

describe("EVENT_TYPE_FAMILIES groups the whole catalogue", () => {
  it("declares a family for every type", () => {
    const undeclared = ALL_TYPES.filter(
      (type) => !ALL_FAMILIES.includes(eventFamily(type)),
    );
    expect(
      undeclared,
      `${undeclared.join(", ")} belong to a family EVENT_TYPE_FAMILIES does ` +
        `not list, so the type filter would drop them.`,
    ).toEqual([]);
  });

  it("declares no family with nothing in it", () => {
    const empty = ALL_FAMILIES.filter(
      (family) => !ALL_TYPES.some((type) => eventFamily(type) === family),
    );
    expect(empty).toEqual([]);
  });
});

describe("isEventType narrows the URL's type param", () => {
  it("accepts every catalogue entry", () => {
    for (const type of ALL_TYPES) {
      expect(isEventType(type), type).toBe(true);
    }
  });

  it("rejects a type the server cannot write, and the 'all' pseudo-value", () => {
    expect(isEventType("bogus.type")).toBe(false);
    expect(isEventType("incident.opened")).toBe(false);
    expect(isEventType("all")).toBe(false);
    expect(isEventType(undefined)).toBe(false);
    expect(isEventType(42)).toBe(false);
  });
});

describe("every filterable type has a translated label", () => {
  const LOCALES = {
    en: eventsEn,
    fr: eventsFr,
    de: eventsDe,
    es: eventsEs,
  };

  const asRecords = (bundle: (typeof LOCALES)[keyof typeof LOCALES]) => ({
    types: bundle.types as Record<string, string>,
    families: bundle.audit.families as Record<string, string>,
  });

  it.each(Object.keys(LOCALES))("%s labels every type, never the raw code", (lang) => {
    const { types } = asRecords(LOCALES[lang as keyof typeof LOCALES]);
    for (const type of ALL_TYPES) {
      expect(types[type], `${type} has no types.${type} label in ${lang}`).toBeTruthy();
      expect(types[type]).not.toBe(type);
    }
  });

  it.each(Object.keys(LOCALES))("%s labels every family heading", (lang) => {
    const { families } = asRecords(LOCALES[lang as keyof typeof LOCALES]);
    for (const family of ALL_FAMILIES) {
      expect(
        families[family],
        `${family} has no audit.families.${family} label in ${lang}`,
      ).toBeTruthy();
    }
  });
});
