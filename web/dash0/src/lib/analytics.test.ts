import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import {
  distinctId,
  fetchPublicConfig,
  identifyAnalytics,
  initAnalytics,
  isAnalyticsEnabled,
  sanitizeProperties,
  scrubPath,
  scrubUrl,
  __resetAnalyticsForTests,
} from "./analytics";

beforeEach(() => {
  __resetAnalyticsForTests();
});

afterEach(() => {
  vi.unstubAllGlobals();
  __resetAnalyticsForTests();
});

describe("isAnalyticsEnabled", () => {
  // The enablement rule must match config.PostHogConfig.Active() on the
  // backend exactly: enabled AND a non-empty project key.
  it("is off for every unconfigured shape", () => {
    expect(isAnalyticsEnabled(null)).toBe(false);
    expect(isAnalyticsEnabled(undefined)).toBe(false);
    expect(isAnalyticsEnabled({})).toBe(false);
    expect(isAnalyticsEnabled({ posthog: { enabled: false } })).toBe(false);
    // The self-hosted default: kill switch on, no key.
    expect(isAnalyticsEnabled({ posthog: { enabled: true } })).toBe(false);
    expect(isAnalyticsEnabled({ posthog: { enabled: true, projectApiKey: "" } })).toBe(false);
    expect(isAnalyticsEnabled({ posthog: { enabled: true, projectApiKey: "   " } })).toBe(false);
    // A key alone never overrides the kill switch.
    expect(isAnalyticsEnabled({ posthog: { enabled: false, projectApiKey: "phc_k" } })).toBe(false);
  });

  it("is on only with both the switch and a key", () => {
    expect(isAnalyticsEnabled({ posthog: { enabled: true, projectApiKey: "phc_k" } })).toBe(true);
  });
});

describe("initAnalytics", () => {
  // The load-bearing negative test: with no credentials, initAnalytics must
  // resolve false WITHOUT importing posthog-js. If it ever imported eagerly,
  // the dynamic import would resolve and this would return true.
  it("loads nothing when analytics is off", async () => {
    for (const config of [
      null,
      {},
      { posthog: { enabled: true } },
      { posthog: { enabled: false, projectApiKey: "phc_k" } },
    ]) {
      expect(await initAnalytics(config)).toBe(false);
    }
  });

  it("identify is a pure no-op when nothing was ever loaded", () => {
    expect(() => identifyAnalytics("org-uid", "user-uid")).not.toThrow();
  });
});

describe("distinctId", () => {
  // MUST stay byte-identical to analytics.DistinctID in
  // server/internal/analytics/analytics.go.
  it("matches the backend scheme", () => {
    expect(distinctId("org-uid", "user-uid")).toBe("org:org-uid/user:user-uid");
    expect(distinctId("org-uid", null)).toBe("org:org-uid");
    expect(distinctId(null, "user-uid")).toBe("user:user-uid");
    expect(distinctId(null, null)).toBe("anonymous");
  });
});

describe("scrubPath", () => {
  it("removes org slugs and resource identifiers", () => {
    expect(scrubPath("/dash0/orgs/acme-corp/checks")).toBe("/dash0/orgs/:org/checks");
    expect(
      scrubPath("/dash0/orgs/acme-corp/checks/8f0e1d2c-3b4a-5968-8776-a5b4c3d2e1f0"),
    ).toBe("/dash0/orgs/:org/checks/:uid");
    expect(scrubPath("/dash0/orgs/acme/status-pages/my-public-page")).toBe(
      "/dash0/orgs/:org/status-pages/:id",
    );
    expect(scrubPath("/dash0/orgs/acme/incidents/42")).toBe("/dash0/orgs/:org/incidents/:id");
  });

  it("leaves static paths alone", () => {
    expect(scrubPath("/dash0/login")).toBe("/dash0/login");
  });
});

describe("scrubUrl", () => {
  it("drops query strings and fragments and scrubs the path", () => {
    expect(
      scrubUrl("https://ping.example.com/dash0/orgs/acme/checks?q=secret-host#frag"),
    ).toBe("https://ping.example.com/dash0/orgs/:org/checks");
  });
});

describe("sanitizeProperties", () => {
  it("scrubs every URL-ish autocapture property", () => {
    const out = sanitizeProperties({
      $current_url: "https://ping.example.com/dash0/orgs/acme/checks/8f0e1d2c-3b4a-5968-8776-a5b4c3d2e1f0?q=x",
      $pathname: "/dash0/orgs/acme/checks/8f0e1d2c-3b4a-5968-8776-a5b4c3d2e1f0",
      $referrer: "$direct",
      other: "kept",
    });

    expect(out.$current_url).toBe("https://ping.example.com/dash0/orgs/:org/checks/:uid");
    expect(out.$pathname).toBe("/dash0/orgs/:org/checks/:uid");
    expect(out.$referrer).toBe("$direct");
    expect(out.other).toBe("kept");
    expect(JSON.stringify(out)).not.toContain("acme");
  });
});

describe("fetchPublicConfig", () => {
  it("returns null instead of throwing when the endpoint is unavailable", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("offline")));
    expect(await fetchPublicConfig()).toBeNull();

    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false }));
    expect(await fetchPublicConfig()).toBeNull();
  });

  it("returns the parsed document", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: async () => ({ posthog: { enabled: false } }),
      }),
    );
    expect(await fetchPublicConfig()).toEqual({ posthog: { enabled: false } });
  });
});
