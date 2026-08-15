import { beforeAll, describe, expect, it } from "vitest";
import i18next, { type TFunction } from "i18next";

import {
  EVENT_TYPE_REGISTRY,
  getEventEmoji,
  getEventLabel,
  getEventTone,
} from "@/components/dashboard/event-display";

import eventsEn from "@/locales/en/events.json";

// A REAL i18next instance (not a hand-rolled dotted-key resolver) backed by
// the actual events.json bundle. This matters because every EventType*
// constant contains its own dots (e.g. "status_update.deleted",
// "org.activation.signup_completed"), and i18next's key resolution for
// `types.${eventType}` — namespace "events", flat key "status_update.deleted"
// inside "types" — is NOT what a naive `key.split(".")` walk would produce.
// Only the real library reproduces what getEventLabel actually gets at
// runtime.
let t: TFunction;

beforeAll(async () => {
  const instance = i18next.createInstance();
  await instance.init({
    lng: "en",
    resources: { en: { events: eventsEn } },
    interpolation: { escapeValue: false },
  });
  t = instance.getFixedT("en", "events");
});

// Every EventType* constant defined in server/internal/db/models/event.go —
// copied here BY HAND, deliberately NOT derived from event-display.tsx's own
// map. A test built from the frontend map would only prove the map agrees
// with itself; this list is the actual guard against a new backend event
// type shipping with no dash0 identity at all. Keep it in sync with
// event.go when a constant is added, renamed, or removed there.
const BACKEND_EVENT_TYPES = [
  "check.created",
  "check.updated",
  "check.deleted",
  "incident.created",
  "incident.escalated",
  "incident.escalation_failed",
  "incident.resolved",
  "incident.reopened",
  "incident.acknowledged",
  "incident.unacknowledged",
  "incident.snoozed",
  "incident.unsnoozed",
  "incident.comment",
  "status_update.created",
  "status_update.updated",
  "status_update.deleted",
  "org.activation.signup_completed",
  "org.activation.first_check_created",
  "org.activation.first_result_received",
  "org.activation.first_notification_configured",
  "org.activation.first_incident_paged",
];

describe("dash0 has an identity for every backend event type", () => {
  it.each(BACKEND_EVENT_TYPES)(
    "%s resolves to a real translated label, not the raw code",
    (eventType) => {
      // getEventLabel falls back to the raw eventType (via defaultValue) when
      // no translation key exists — that silent fallback is exactly what a
      // new event type shipping without an identity would look like.
      const label = getEventLabel(eventType, t);
      expect(label).not.toBe(eventType);
      expect(label.length).toBeGreaterThan(0);
    },
  );

  it.each(BACKEND_EVENT_TYPES)(
    "%s resolves a tone class without throwing",
    (eventType) => {
      expect(() => getEventTone(eventType)).not.toThrow();
    },
  );
});

// The 8 binding emoji picks from the spec's "Resolved open questions"
// (2026-08-15-02) — exactly one emoji per event type, product-wide, matching
// server/internal/notifications/msteamsbot.go and
// server/internal/integrations/telegram/message.go.
describe("EVENT_TYPE_REGISTRY pins the binding emoji per event type", () => {
  const BINDING_PAIRS: [string, string][] = [
    ["incident.created", "🔴"],
    ["incident.reopened", "🔁"],
    ["incident.escalated", "⚠️"],
    ["incident.escalation_failed", "❌"],
    ["incident.resolved", "🟢"],
    ["incident.acknowledged", "✅"],
    ["incident.unacknowledged", "↩️"],
    ["incident.snoozed", "💤"],
  ];

  it.each(BINDING_PAIRS)("%s pairs with %s", (eventType, emoji) => {
    expect(getEventEmoji(eventType)).toBe(emoji);
    expect(EVENT_TYPE_REGISTRY[eventType]?.emoji).toBe(emoji);
  });

  it("has no registry entries outside the binding list above", () => {
    const bound = new Set(BINDING_PAIRS.map(([eventType]) => eventType));
    expect(Object.keys(EVENT_TYPE_REGISTRY).sort()).toEqual(
      [...bound].sort(),
    );
  });

  it("every registered event type is a real backend constant", () => {
    for (const eventType of Object.keys(EVENT_TYPE_REGISTRY)) {
      expect(BACKEND_EVENT_TYPES).toContain(eventType);
    }
  });
});

describe("an unmapped event type still degrades gracefully", () => {
  it("has no emoji and the neutral outline tone", () => {
    expect(getEventEmoji("something.unmapped")).toBeUndefined();
    expect(getEventTone("something.unmapped")).toBe("");
  });
});
