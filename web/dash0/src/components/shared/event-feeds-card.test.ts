import { describe, expect, it } from "vitest";

import en from "@/locales/en/org.json";
import fr from "@/locales/fr/org.json";
import de from "@/locales/de/org.json";
import es from "@/locales/es/org.json";
import { scopeKey } from "@/lib/event-feeds";

// The scope choices the server offers (eventfeeds.ScopeChoices).
const SCOPE_CHOICES = [
  "incident.*",
  "check.created",
  "check.updated",
  "check.deleted",
  "status_update.*",
  "maintenance_window.*",
];

describe("event feeds card", () => {
  it("maps scope choices to i18n keys", () => {
    expect(scopeKey("incident.*")).toBe("incident_all");
    expect(scopeKey("check.created")).toBe("check_created");
    expect(scopeKey("maintenance_window.*")).toBe("maintenance_window_all");
  });

  it("labels every scope choice in every language", () => {
    for (const catalog of [en, fr, de, es]) {
      const labels = catalog.settings.eventFeeds.scope as Record<string, string>;
      for (const choice of SCOPE_CHOICES) {
        expect(labels[scopeKey(choice)], choice).toBeTruthy();
      }
    }
  });
});
