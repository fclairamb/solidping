import { readFileSync } from "node:fs";
import { join } from "node:path";
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

// EVENT_GO_PATH resolves server/internal/db/models/event.go from THIS test
// file's own location, so it works both from the repo root and from
// web/dash0 (vitest's cwd). event-display.test.ts lives at
// web/dash0/src/components/dashboard/ — five levels below the repo root.
const EVENT_GO_PATH = join(
  import.meta.dirname,
  "../../../../..",
  "server/internal/db/models/event.go",
);

// parseBackendEventTypes extracts every `EventTypeXxx EventType = "..."`
// constant value from the REAL Go source at test time — deliberately NOT a
// list copied into this file, which would go stale silently. This is the
// actual guard the spec asked for: add a new EventType* constant to
// event.go and it flows into this suite automatically, with no hand-edit
// required on the frontend side.
function parseBackendEventTypes(): string[] {
  const source = readFileSync(EVENT_GO_PATH, "utf-8");
  const matches = source.matchAll(/EventType\w+\s+EventType\s*=\s*"([^"]+)"/g);
  return [...matches].map((m) => m[1]);
}

const BACKEND_EVENT_TYPES = parseBackendEventTypes();

// Event types that intentionally have NO EVENT_TYPE_REGISTRY entry — the
// family fallback rules in getEventTone/getEventIcon cover them (a tone and
// a lucide icon, no specific emoji), which is an accepted, documented
// outcome for these. Every backend constant must be either registered here
// with a reason, or present in EVENT_TYPE_REGISTRY — see the "every backend
// event type is accounted for" test below, which is what actually enforces
// this and fails with the offending constant's name when neither holds.
const INTENTIONALLY_UNMAPPED: Record<string, string> = {
  "check.created": "configuration change — family fallback (blue) is enough",
  "check.updated": "configuration change — family fallback (blue) is enough",
  "check.deleted": "configuration change — family fallback (blue) is enough",
  "incident.unsnoozed": "minor lifecycle event — amber family fallback is enough",
  "status_update.created": "status-page activity — family fallback (blue) is enough",
  "status_update.updated": "status-page activity — family fallback (blue) is enough",
  "status_update.deleted": "status-page activity — family fallback (blue) is enough",
  "org.activation.signup_completed": "onboarding milestone — family fallback (violet) is enough",
  "org.activation.first_check_created": "onboarding milestone — family fallback (violet) is enough",
  "org.activation.first_result_received": "onboarding milestone — family fallback (violet) is enough",
  "org.activation.first_notification_configured":
    "onboarding milestone — family fallback (violet) is enough",
  "org.activation.first_incident_paged": "onboarding milestone — family fallback (violet) is enough",
};

describe("parseBackendEventTypes (positive control)", () => {
  // Guards against the regex silently matching nothing (a vacuously-passing
  // guard is worse than no guard — it would look green forever).
  it("actually found constants in event.go", () => {
    expect(BACKEND_EVENT_TYPES.length).toBeGreaterThan(15);
  });

  it("found known constants by their string value", () => {
    expect(BACKEND_EVENT_TYPES).toContain("incident.created");
    expect(BACKEND_EVENT_TYPES).toContain("incident.resolved");
    expect(BACKEND_EVENT_TYPES).toContain("check.created");
  });
});

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

  it("every backend event type is either registered or explicitly, intentionally unmapped", () => {
    const unaccounted = BACKEND_EVENT_TYPES.filter(
      (eventType) =>
        !(eventType in EVENT_TYPE_REGISTRY) &&
        !(eventType in INTENTIONALLY_UNMAPPED),
    );

    expect(
      unaccounted,
      `event.go defines ${unaccounted.join(", ")} with no dash0 identity: ` +
        `add it to EVENT_TYPE_REGISTRY in event-display.tsx (give it an ` +
        `emoji+tone), or to INTENTIONALLY_UNMAPPED in this test with a ` +
        `one-line reason the family fallback is enough.`,
    ).toEqual([]);
  });
});

// The binding emoji picks from the spec's "Resolved open questions"
// (2026-08-15-02) — exactly one emoji per event type, product-wide, matching
// server/internal/notifications/msteamsbot.go, slack.go and
// server/internal/integrations/telegram/message.go. `incident.comment` joined
// the list when comments started fanning out through the notification
// pipeline: its 💬 is `commentEmoji` in server/internal/notifications/comment.go,
// which names this registry as the other half of the pairing.
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
    ["incident.comment", "💬"],
    // Status-page publication lifecycle (spec 2026-08-19-08). Unlike the
    // incident.* pairs above these have NO backend chat-integration
    // counterpart to stay aligned with — publication events fan out to
    // `webhook` connections only (see incidentpublications/webhooks.go), and
    // the chat integrations deliberately stay on the internal incident.*
    // lifecycle. So dash0 is the sole owner of this pairing.
    ["statuspage.incident.published", "📣"],
    ["statuspage.incident.updated", "📝"],
    ["statuspage.incident.resolved", "📗"],
    // Delivery circuit breaker (spec 2026-08-21-07). Same ownership story as
    // the three above: no backend chat integration hand-authors a message for
    // this type, so dash0 is the sole owner of the pairing.
    ["statuspage.subscriber.disabled", "🔇"],
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

  it("INTENTIONALLY_UNMAPPED and EVENT_TYPE_REGISTRY never overlap", () => {
    for (const eventType of Object.keys(INTENTIONALLY_UNMAPPED)) {
      expect(EVENT_TYPE_REGISTRY[eventType]).toBeUndefined();
    }
  });
});

describe("an unmapped event type still degrades gracefully", () => {
  it("has no emoji and the neutral outline tone", () => {
    expect(getEventEmoji("something.unmapped")).toBeUndefined();
    expect(getEventTone("something.unmapped")).toBe("");
  });
});
