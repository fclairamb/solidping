import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import {
  distinctId,
  fetchPublicConfig,
  identifyAnalytics,
  initAnalytics,
  isAnalyticsEnabled,
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

  // An identify that lands before /api/v1/config resolves must be queued, not
  // dropped — a restored session identifies on boot, usually ahead of the
  // config round trip.
  it("queues an early identify and replays it once the config enables analytics", async () => {
    identifyAnalytics("org-uid", "user-uid");

    const identified: string[] = [];
    vi.doMock("./posthog-loader", () => ({
      default: {
        init: () => {},
        identify: (id: string) => identified.push(id),
        reset: () => {},
        capture: () => {},
      },
    }));

    expect(
      await initAnalytics({ posthog: { enabled: true, projectApiKey: "phc_k" } }),
    ).toBe(true);

    expect(identified).toEqual(["org:org-uid/user:user-uid"]);
    vi.doUnmock("./posthog-loader");
  });

  // The dashboard must default api_host to the first-party proxy path (so ad
  // blockers do not drop events) and pass ui_host through so in-app links still
  // resolve to the PostHog app.
  it("initializes posthog with the first-party proxy path and ui_host", async () => {
    let options: Record<string, unknown> | undefined;
    vi.doMock("./posthog-loader", () => ({
      default: {
        init: (_key: string, opts: Record<string, unknown>) => {
          options = opts;
        },
        identify: () => {},
        reset: () => {},
        capture: () => {},
      },
    }));

    expect(
      await initAnalytics({
        posthog: {
          enabled: true,
          projectApiKey: "phc_k",
          host: "/ingest",
          uiHost: "https://eu.posthog.com",
        },
      }),
    ).toBe(true);

    expect(options?.api_host).toBe("/ingest");
    expect(options?.ui_host).toBe("https://eu.posthog.com");
    vi.doUnmock("./posthog-loader");
  });

  // With no ui_host from the backend (an operator-configured host), the option
  // must be omitted so posthog-js derives it from api_host as before.
  it("omits ui_host when the backend does not send one", async () => {
    let options: Record<string, unknown> | undefined;
    vi.doMock("./posthog-loader", () => ({
      default: {
        init: (_key: string, opts: Record<string, unknown>) => {
          options = opts;
        },
        identify: () => {},
        reset: () => {},
        capture: () => {},
      },
    }));

    expect(
      await initAnalytics({
        posthog: { enabled: true, projectApiKey: "phc_k", host: "https://ph.example.com" },
      }),
    ).toBe(true);

    expect(options?.api_host).toBe("https://ph.example.com");
    expect("ui_host" in (options ?? {})).toBe(false);
    vi.doUnmock("./posthog-loader");
  });

  // Spec 2026-09-25-11: the replay network plugin recorded the navigation entry
  // of the OAuth handoff with its original URL, tokens included. initAnalytics
  // must wire both credential filters, and they must actually filter.
  it("wires credential redaction into replay network capture and before_send", async () => {
    let options: Record<string, unknown> | undefined;
    vi.doMock("./posthog-loader", () => ({
      default: {
        init: (_key: string, opts: Record<string, unknown>) => {
          options = opts;
        },
        identify: () => {},
        reset: () => {},
        capture: () => {},
      },
    }));

    expect(
      await initAnalytics({ posthog: { enabled: true, projectApiKey: "phc_k" } }),
    ).toBe(true);
    vi.doUnmock("./posthog-loader");

    // Replay stays on: this is a credentials filter, not a return to masking.
    expect(options?.disable_session_recording).toBe(false);

    // Header and body capture are pinned off client-side, whatever the
    // PostHog project enables: a mask fn disables posthog-js's own body
    // scrubber, so a remote toggle must not be able to start recording them.
    const recording = options?.session_recording as Record<string, unknown> | undefined;
    expect(recording?.recordBody).toBe(false);
    expect(recording?.recordHeaders).toBe(false);

    const url = "https://solidping.example/d/orgs/acme?access_token=a&refresh_token=b&org=acme";
    const leaks = (s: string) => /access_token=a(&|$)/.test(s) || /refresh_token=b(&|$)/.test(s);

    // Positive control: what PostHog received before this fix (no hook, the
    // entry as recorded) does contain both tokens, so the assertions below
    // are not vacuous.
    const navigationEntry = { name: url, entryType: "navigation", initiatorType: "navigation" };
    expect(leaks(navigationEntry.name)).toBe(true);

    const mask = (options?.session_recording as Record<string, unknown> | undefined)
      ?.maskCapturedNetworkRequestFn as ((r: typeof navigationEntry) => typeof navigationEntry) | undefined;
    expect(typeof mask).toBe("function");
    const masked = mask!(navigationEntry);
    expect(leaks(masked.name)).toBe(false);
    expect(masked.name).toContain("org=acme");
    expect(masked.entryType).toBe("navigation");

    const beforeSend = options?.before_send as
      | Array<(e: Record<string, unknown> | null) => Record<string, unknown> | null>
      | undefined;
    expect(Array.isArray(beforeSend)).toBe(true);
    const event = {
      event: "$pageview",
      properties: { $current_url: url, $referrer: url },
      $set_once: { $initial_current_url: url, $initial_referrer: url },
    };
    expect(leaks(JSON.stringify(event))).toBe(true);
    const sent = beforeSend!.reduce<Record<string, unknown> | null>((e, fn) => fn(e), event);
    expect(sent).not.toBeNull();
    expect(leaks(JSON.stringify(sent))).toBe(false);
    expect(JSON.stringify(sent)).toContain("org=acme");
  });

  // …and when analytics stays off, that queued identity is discarded, never sent.
  it("discards a queued identify when analytics is off", async () => {
    identifyAnalytics("org-uid", "user-uid");
    expect(await initAnalytics({ posthog: { enabled: true } })).toBe(false);

    const identified: string[] = [];
    vi.doMock("./posthog-loader", () => ({
      default: {
        init: () => {},
        identify: (id: string) => identified.push(id),
        reset: () => {},
        capture: () => {},
      },
    }));

    expect(
      await initAnalytics({ posthog: { enabled: true, projectApiKey: "phc_k" } }),
    ).toBe(true);

    expect(identified).toEqual([]);
    vi.doUnmock("./posthog-loader");
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
