import { describe, expect, it } from "vitest";

import serverDe from "@/locales/de/server.json";
import serverEn from "@/locales/en/server.json";
import serverEs from "@/locales/es/server.json";
import serverFr from "@/locales/fr/server.json";
import eventsDe from "@/locales/de/events.json";
import eventsEn from "@/locales/en/events.json";
import eventsEs from "@/locales/es/events.json";
import eventsFr from "@/locales/fr/events.json";

// Super-admin impersonation (spec 2026-09-29-03). The banner is the only thing
// telling an admin they are acting as someone else, and the confirm dialog is
// where they are told it is audited: a missing key would render a raw dotted
// path in exactly those two places.

const LOCALES = [
  ["en", serverEn, eventsEn],
  ["fr", serverFr, eventsFr],
  ["de", serverDe, eventsDe],
  ["es", serverEs, eventsEs],
] as const;

const SERVER_KEYS = [
  "users.columns.actions",
  "users.impersonate.button",
  "users.impersonate.title",
  "users.impersonate.description",
  "users.impersonate.organization",
  "users.impersonate.confirm",
  "users.impersonate.cancel",
  "users.impersonate.error",
  "impersonation.banner.title",
  "impersonation.banner.description",
  "impersonation.banner.exit",
  "impersonation.forbidden",
];

function lookup(bundle: unknown, path: string): unknown {
  return path
    .split(".")
    .reduce<unknown>(
      (node, segment) =>
        node && typeof node === "object"
          ? (node as Record<string, unknown>)[segment]
          : undefined,
      bundle,
    );
}

describe("impersonation copy locale parity", () => {
  it.each(LOCALES)("%s carries every impersonation key", (_locale, server, events) => {
    for (const key of SERVER_KEYS) {
      const value = lookup(server, key);
      expect(value, `${key} is missing`).toBeTypeOf("string");
      expect(String(value).trim(), `${key} is empty`).not.toBe("");
    }

    const types = (events as { types?: Record<string, unknown> }).types ?? {};
    expect(types["auth.impersonation_started"], "events label").toBeTypeOf("string");
  });

  it.each(LOCALES)("%s keeps the placeholders", (_locale, server) => {
    expect(lookup(server, "users.impersonate.title")).toContain("{{email}}");
    expect(lookup(server, "impersonation.banner.title")).toContain("{{email}}");
    expect(lookup(server, "impersonation.banner.description")).toContain("{{time}}");
  });
});
