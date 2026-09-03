import { describe, expect, it } from "vitest";

import serverDe from "@/locales/de/server.json";
import serverEn from "@/locales/en/server.json";
import serverEs from "@/locales/es/server.json";
import serverFr from "@/locales/fr/server.json";

// The Server -> Notifications tab (routes/orgs/$org/server.notifications.tsx)
// is a super-admin page whose copy carries two load-bearing warnings: "this
// recipient has no notification route, nothing will be delivered" and "this
// recipient is no longer a super admin". A missing key would render the raw
// dotted path on exactly the screen an operator reads to convince themselves
// the paging setup works — so all four locales must carry every key, as real
// non-empty strings.

const SERVER_LOCALES = [
  ["en", serverEn],
  ["fr", serverFr],
  ["de", serverDe],
  ["es", serverEs],
] as const;

const NOTIFICATION_KEYS = [
  "tabs.notifications",
  "notifications.title",
  "notifications.description",
  "notifications.enabled",
  "notifications.enabledHelp",
  "notifications.columnUser",
  "notifications.columnRoutes",
  "notifications.events.supportMessage",
  "notifications.events.userRegistered",
  "notifications.noRoutes",
  "notifications.noRecipients",
  "notifications.noSuperAdmins",
  "notifications.notSuperAdmin",
  "notifications.superAdminOnly",
  "notifications.sendTest",
  "notifications.testDelivered_one",
  "notifications.testDelivered_other",
  "notifications.testUndeliverable",
];

// Keys are looked up by walking segments, the way i18next resolves a dotted
// path.
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

describe("operator notifications copy locale parity", () => {
  it.each(SERVER_LOCALES)(
    "%s carries every operator notification key",
    (_locale, bundle) => {
      for (const key of NOTIFICATION_KEYS) {
        const value = lookup(bundle, key);
        expect(value, `${key} is missing`).toBeTypeOf("string");
        expect(String(value).trim(), `${key} is empty`).not.toBe("");
      }
    },
  );

  // The backend event names carry dots, which i18next reads as path
  // separators, so the page maps them onto these camelCase keys
  // (EVENT_LABEL_KEY in server.notifications.tsx). Every subscribable event
  // must have one, or its column header renders as a raw event name.
  it("carries one label per subscribable event, and no stragglers", () => {
    for (const [, bundle] of SERVER_LOCALES) {
      const events = lookup(bundle, "notifications.events") as Record<
        string,
        unknown
      >;
      expect(Object.keys(events).sort()).toEqual([
        "supportMessage",
        "userRegistered",
      ]);
    }
  });

  it("translates rather than copying English into every locale", () => {
    const english = String(lookup(serverEn, "notifications.noRoutes"));
    for (const [locale, bundle] of SERVER_LOCALES) {
      if (locale === "en") continue;
      expect(
        String(lookup(bundle, "notifications.noRoutes")),
        `${locale} still carries the English string`,
      ).not.toBe(english);
    }
  });
});
