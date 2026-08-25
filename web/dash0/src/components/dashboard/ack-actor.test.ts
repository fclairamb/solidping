import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

import {
  getAckActor,
  getEventActorLabel,
  getEventVia,
  isAckEvent,
} from "@/components/dashboard/event-display";

const ACK = "incident.acknowledged";

describe("getAckActor", () => {
  it("reads the SNAKE_CASE keys the backend actually writes", () => {
    // The regression this whole helper exists for: the dashboard used to read
    // camelCase keys, which the acknowledgment event does not write, so every
    // Slack/Discord/phone ack rendered with no actor at all.
    expect(
      getAckActor({ eventType: ACK, payload: { slack_username: "alice" } }),
    ).toBe("alice");
    expect(
      getAckActor({ eventType: ACK, payload: { discord_username: "bob" } }),
    ).toBe("bob");
    expect(
      getAckActor({
        eventType: ACK,
        payload: { acknowledged_by_telegram: "via Telegram (Carol)" },
      }),
    ).toBe("via Telegram (Carol)");
    expect(
      getAckActor({
        eventType: ACK,
        payload: { acknowledged_by_email: "alice@acme.com" },
      }),
    ).toBe("alice@acme.com");
    expect(
      getAckActor({
        eventType: ACK,
        payload: { acknowledged_by_phone: "+33123456789" },
      }),
    ).toBe("+33123456789");
  });

  it("is a NEGATIVE control on camelCase — those keys must not match", () => {
    // Positive control above proves the extraction works; this proves it is
    // reading the real keys rather than accidentally matching everything.
    expect(
      getAckActor({ eventType: ACK, payload: { slackUsername: "alice" } }),
    ).toBeUndefined();
    expect(
      getAckActor({
        eventType: ACK,
        payload: { acknowledgedByEmail: "alice@acme.com" },
      }),
    ).toBeUndefined();
  });

  it("prefers the most specific identity and falls back to the raw id", () => {
    expect(
      getAckActor({
        eventType: ACK,
        payload: {
          slack_user_id: "U123",
          slack_username: "alice",
          acknowledged_by_email: "alice@acme.com",
        },
      }),
    ).toBe("alice");

    expect(
      getAckActor({ eventType: ACK, payload: { slack_user_id: "U123" } }),
    ).toBe("U123");
  });

  it("ignores blank values and non-ack events", () => {
    expect(
      getAckActor({ eventType: ACK, payload: { slack_username: "   " } }),
    ).toBeUndefined();
    expect(
      getAckActor({
        eventType: "incident.comment",
        payload: { slack_username: "alice" },
      }),
    ).toBeUndefined();
    expect(getAckActor({ eventType: ACK })).toBeUndefined();
  });
});

describe("isAckEvent / getEventVia", () => {
  it("identifies the ack event and its originating channel", () => {
    expect(isAckEvent({ eventType: ACK })).toBe(true);
    expect(isAckEvent({ eventType: "incident.unacknowledged" })).toBe(false);
    expect(getEventVia({ payload: { via: "slack" } })).toBe("slack");
    expect(getEventVia({ payload: {} })).toBeUndefined();
    expect(getEventVia({ payload: { via: 42 } })).toBeUndefined();
  });
});

describe("getEventActorLabel", () => {
  it("prefers the API-resolved name, then the ack payload, then the type", () => {
    expect(
      getEventActorLabel({
        eventType: ACK,
        actorType: "user",
        actorName: "Alice Acme",
        payload: { slack_username: "alice" },
      }),
    ).toBe("Alice Acme");

    // The case the FK cannot express: a Slack acker has no users row, so
    // actorName/actorUid are absent and the payload is the only record.
    expect(
      getEventActorLabel({
        eventType: ACK,
        actorType: "user",
        payload: { slack_username: "alice" },
      }),
    ).toBe("alice");

    expect(
      getEventActorLabel({
        eventType: ACK,
        actorType: "user",
        actorEmail: "bob@acme.com",
        payload: {},
      }),
    ).toBe("bob@acme.com");

    expect(getEventActorLabel({ eventType: ACK, actorType: "system" })).toBe(
      "system",
    );

    expect(getEventActorLabel({ eventType: ACK })).toBeUndefined();
  });
});

// The backend writes these keys; drift on either side silently reproduces the
// original bug (an acknowledgment with no visible actor), so the test reads the
// REAL Go source rather than a list copied into this file.
const ACK_ACTOR_GO_PATH = join(
  import.meta.dirname,
  "../../../../..",
  "server/internal/handlers/incidents/ack_actor.go",
);

describe("payload key parity with the backend", () => {
  it("every ack payload key the server writes is one this file extracts", () => {
    const source = readFileSync(ACK_ACTOR_GO_PATH, "utf-8");
    const declared = [
      ...source.matchAll(/payloadKeyAck\w+\s+=\s+"([^"]+)"/g),
    ].map((m) => m[1]);

    // Positive control: this really is the constant block.
    expect(declared).toContain("slack_username");
    expect(declared.length).toBeGreaterThanOrEqual(7);

    // `note` is content, not identity, and is deliberately not an actor key.
    const identityKeys = declared.filter((key) => key !== "note");

    for (const key of identityKeys) {
      expect(
        getAckActor({ eventType: ACK, payload: { [key]: "someone" } }),
        `payload key ${key} must be extractable as an ack actor`,
      ).toBe("someone");
    }
  });
});
