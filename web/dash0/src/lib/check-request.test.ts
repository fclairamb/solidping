import { describe, expect, it } from "vitest";
import type { CheckFormData } from "@/components/shared/check-form";
import { toCreateCheckRequest, toUpdateCheckRequest } from "./check-request";

/**
 * These tests are about the REQUEST BODY, not the form's internal state — which
 * is exactly the distinction the bug they guard turned on. The degraded-detection
 * section rendered, `buildDegradedPayload` put all six fields into the form's
 * `data` object, Save reported success, and the PATCH never carried them, because
 * the route hand-picked which keys of `data` to forward. Asserting the form's
 * payload builder was not enough; asserting what the routes send is.
 */

/** A payload shaped like the one CheckForm hands its `onSubmit`. */
function formData(overrides: Partial<CheckFormData> = {}): CheckFormData {
  return {
    type: "http",
    enabled: true,
    name: "acme",
    slug: "acme",
    period: "00:01:00",
    config: { url: "https://acme.com" },
    regions: ["default"],
    tracerouteOnFailure: "inherit",
    confirmationPeriodSeconds: 120,
    recoveryPeriodSeconds: 120,
    degradedFailures: 5,
    degradedFailuresWindow: 60,
    degradedSlow: 3,
    degradedSlowWindow: 6,
    slowThresholdMs: 900,
    degradedEnabled: true,
    connectionUids: ["conn-1"],
    dependsOn: [{ parentCheckUid: "parent-1", kind: "hard", description: "" }],
    initialDependsOn: [],
    ...overrides,
  };
}

const DEGRADED_KEYS = [
  "degradedFailures",
  "degradedFailuresWindow",
  "degradedSlow",
  "degradedSlowWindow",
  "slowThresholdMs",
  "degradedEnabled",
] as const;

describe("toUpdateCheckRequest", () => {
  it("forwards every degraded-detection field to the PATCH body", () => {
    const body = toUpdateCheckRequest(formData()) as Record<string, unknown>;

    for (const key of DEGRADED_KEYS) {
      expect(body, `${key} must reach the server`).toHaveProperty(key);
    }

    expect(body.degradedFailures).toBe(5);
    expect(body.degradedFailuresWindow).toBe(60);
    expect(body.degradedSlow).toBe(3);
    expect(body.degradedSlowWindow).toBe(6);
    expect(body.slowThresholdMs).toBe(900);
    expect(body.degradedEnabled).toBe(true);
  });

  it("forwards a 0 threshold — turning the slow rule off must be savable", () => {
    const body = toUpdateCheckRequest(
      formData({ slowThresholdMs: 0, degradedFailures: 0 }),
    ) as Record<string, unknown>;

    expect(body.slowThresholdMs).toBe(0);
    expect(body.degradedFailures).toBe(0);
  });

  it("forwards degradedEnabled false rather than dropping it", () => {
    const body = toUpdateCheckRequest(
      formData({ degradedEnabled: false }),
    ) as Record<string, unknown>;

    expect(body).toHaveProperty("degradedEnabled");
    expect(body.degradedEnabled).toBe(false);
  });

  it("keeps carrying the fields an earlier regression dropped", () => {
    // spec 2026-07-15-04 lost these the same way. They are asserted here so the
    // deny-list can never quietly become a shorter allow-list again.
    const body = toUpdateCheckRequest(formData()) as Record<string, unknown>;

    expect(body.confirmationPeriodSeconds).toBe(120);
    expect(body.recoveryPeriodSeconds).toBe(120);
    expect(body.period).toBe("00:01:00");
    expect(body.enabled).toBe(true);
    expect(body.config).toEqual({ url: "https://acme.com" });
  });

  it("excludes the three fields that travel through their own endpoints", () => {
    const body = toUpdateCheckRequest(formData()) as Record<string, unknown>;

    expect(body).not.toHaveProperty("connectionUids");
    expect(body).not.toHaveProperty("dependsOn");
    expect(body).not.toHaveProperty("initialDependsOn");
  });

  it("omits `type`, which a PATCH cannot change", () => {
    // A check's type is immutable once it exists, and UpdateCheckRequest has no
    // such field. The fixture sets one, so this is a real exclusion rather than
    // an absence that happens to hold.
    const body = toUpdateCheckRequest(formData()) as Record<string, unknown>;

    expect(body).not.toHaveProperty("type");
    // ...while create, which is where the type is decided, still carries it.
    expect(
      toCreateCheckRequest(formData()) as Record<string, unknown>,
    ).toHaveProperty("type", "http");
  });
});

describe("toCreateCheckRequest", () => {
  it("forwards every degraded-detection field to the POST body", () => {
    const body = toCreateCheckRequest(formData()) as Record<string, unknown>;

    for (const key of DEGRADED_KEYS) {
      expect(body, `${key} must reach the server`).toHaveProperty(key);
    }
  });

  it("always sends an object config, even when the form left it unset", () => {
    const body = toCreateCheckRequest(
      formData({ config: undefined }),
    ) as Record<string, unknown>;

    expect(body.config).toEqual({});
  });

  it("omits an empty escalation policy and traceroute rather than sending blanks", () => {
    // On create, empty means "not chosen" — sending "" would read as an explicit
    // clear, which is only meaningful on an edit.
    const body = toCreateCheckRequest(
      formData({ escalationPolicyUid: "", tracerouteOnFailure: "" }),
    ) as Record<string, unknown>;

    expect(body).not.toHaveProperty("escalationPolicyUid");
    expect(body).not.toHaveProperty("tracerouteOnFailure");
  });

  it("keeps a chosen escalation policy and traceroute policy", () => {
    const body = toCreateCheckRequest(
      formData({ escalationPolicyUid: "policy-1", tracerouteOnFailure: "on" }),
    ) as Record<string, unknown>;

    expect(body.escalationPolicyUid).toBe("policy-1");
    expect(body.tracerouteOnFailure).toBe("on");
  });

  it("excludes the three fields that travel through their own endpoints", () => {
    const body = toCreateCheckRequest(formData()) as Record<string, unknown>;

    expect(body).not.toHaveProperty("connectionUids");
    expect(body).not.toHaveProperty("dependsOn");
    expect(body).not.toHaveProperty("initialDependsOn");
  });
});
