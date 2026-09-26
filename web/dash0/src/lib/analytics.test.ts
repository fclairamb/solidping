import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import {
  captureException,
  dedupeAutocapturedBoundaryExceptions,
  distinctId,
  dropKnownExceptionNoise,
  fetchPublicConfig,
  identifyAnalytics,
  initAnalytics,
  isAnalyticsEnabled,
  KNOWN_EXCEPTION_NOISE,
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

  // Spec 2026-09-25-13: the Solidping PostHog project had never received a
  // single $exception event because posthog-js's exception autocapture was
  // never turned on. Code config must set all three capture_exceptions
  // flags, not rely on the project's autocapture_exceptions_opt_in toggle.
  it("turns on exception autocapture: unhandled errors, rejections and console errors", async () => {
    let options: Record<string, unknown> | undefined;
    vi.doMock("./posthog-loader", () => ({
      default: {
        init: (_key: string, opts: Record<string, unknown>) => {
          options = opts;
        },
        identify: () => {},
        reset: () => {},
        capture: () => {},
        captureException: () => {},
      },
    }));

    expect(
      await initAnalytics({ posthog: { enabled: true, projectApiKey: "phc_k" } }),
    ).toBe(true);
    vi.doUnmock("./posthog-loader");

    expect(options?.capture_exceptions).toEqual({
      capture_unhandled_errors: true,
      capture_unhandled_rejections: true,
      capture_console_errors: true,
    });
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

describe("captureException", () => {
  it("is a pure no-op when analytics was never initialized", () => {
    expect(() => captureException(new Error("boom"))).not.toThrow();
  });

  it("forwards to the client once initialized", async () => {
    const captured: Array<{ error: unknown; properties?: Record<string, unknown> }> = [];
    vi.doMock("./posthog-loader", () => ({
      default: {
        init: () => {},
        identify: () => {},
        reset: () => {},
        capture: () => {},
        captureException: (error: unknown, properties?: Record<string, unknown>) => {
          captured.push({ error, properties });
        },
      },
    }));

    expect(
      await initAnalytics({ posthog: { enabled: true, projectApiKey: "phc_k" } }),
    ).toBe(true);
    vi.doUnmock("./posthog-loader");

    const error = new Error("boom");
    captureException(error, { componentStack: "at Foo" });
    expect(captured).toEqual([{ error, properties: { componentStack: "at Foo" } }]);

    // No properties supplied: still forwards, with an empty object rather
    // than undefined (matches captureEvent's contract).
    captureException(error);
    expect(captured[1]).toEqual({ error, properties: {} });
  });
});

describe("dropKnownExceptionNoise", () => {
  it("has at least the scanner-noise pattern from the 2026-09-25 investigation", () => {
    expect(KNOWN_EXCEPTION_NOISE.length).toBeGreaterThan(0);
  });

  // The exact nightly noise signature: CefSharp-based Microsoft Safe
  // Links-style email crawlers opening forgot-password/login links.
  it("drops the scanner-noise $exception event", () => {
    const event = {
      event: "$exception",
      properties: {
        $exception_list: [
          {
            type: "Error",
            value: "Object Not Found Matching Id:5, MethodName:update, ParamCount:4",
          },
        ],
      },
    };
    expect(dropKnownExceptionNoise(event)).toBeNull();
  });

  // Positive control: a normal application error, which must never be
  // dropped, so the assertion above is not vacuous.
  it("passes a normal exception through unchanged", () => {
    const event = {
      event: "$exception",
      properties: {
        $exception_list: [{ type: "TypeError", value: "x is undefined" }],
      },
    };
    expect(dropKnownExceptionNoise(event)).toEqual(event);
  });

  it("leaves non-exception events and null alone", () => {
    const event = { event: "$pageview", properties: {} };
    expect(dropKnownExceptionNoise(event)).toEqual(event);
    expect(dropKnownExceptionNoise(null)).toBeNull();
  });
});

describe("dedupeAutocapturedBoundaryExceptions", () => {
  function autocapturedEvent(type: string, value: string) {
    return {
      event: "$exception",
      properties: {
        $exception_list: [{ type, value, mechanism: { handled: false, type: "generic" } }],
      },
    };
  }

  it("drops the autocaptured duplicate of an error just reported via captureException", async () => {
    vi.doMock("./posthog-loader", () => ({
      default: {
        init: () => {},
        identify: () => {},
        reset: () => {},
        capture: () => {},
        captureException: () => {},
      },
    }));
    expect(
      await initAnalytics({ posthog: { enabled: true, projectApiKey: "phc_k" } }),
    ).toBe(true);
    vi.doUnmock("./posthog-loader");

    // Mirrors componentDidCatch: captureException runs first, console.error
    // (and therefore the autocaptured duplicate) right after.
    captureException(new TypeError("x is undefined"), { componentStack: "at Foo" });

    const autocaptured = autocapturedEvent("TypeError", "x is undefined");
    expect(dedupeAutocapturedBoundaryExceptions(autocaptured)).toBeNull();

    // Each explicit report only cancels ONE matching duplicate.
    expect(dedupeAutocapturedBoundaryExceptions(autocapturedEvent("TypeError", "x is undefined"))).toEqual(
      autocapturedEvent("TypeError", "x is undefined"),
    );
  });

  // Positive control: an autocaptured exception that was never explicitly
  // reported (a real unhandled error, a console.error elsewhere in the app)
  // must pass through — otherwise real errors go missing.
  it("passes through an autocaptured exception nothing explicitly reported", () => {
    const autocaptured = autocapturedEvent("ReferenceError", "foo is not defined");
    expect(dedupeAutocapturedBoundaryExceptions(autocaptured)).toEqual(autocaptured);
  });

  it("never touches an explicitly-reported (mechanism.handled: true) exception", async () => {
    vi.doMock("./posthog-loader", () => ({
      default: {
        init: () => {},
        identify: () => {},
        reset: () => {},
        capture: () => {},
        captureException: () => {},
      },
    }));
    expect(
      await initAnalytics({ posthog: { enabled: true, projectApiKey: "phc_k" } }),
    ).toBe(true);
    vi.doUnmock("./posthog-loader");
    captureException(new TypeError("x is undefined"));

    const explicit = {
      event: "$exception",
      properties: {
        $exception_list: [
          { type: "TypeError", value: "x is undefined", mechanism: { handled: true, type: "generic" } },
        ],
      },
    };
    expect(dedupeAutocapturedBoundaryExceptions(explicit)).toEqual(explicit);
  });

  it("leaves non-exception events and null alone", () => {
    const event = { event: "$pageview", properties: {} };
    expect(dedupeAutocapturedBoundaryExceptions(event)).toEqual(event);
    expect(dedupeAutocapturedBoundaryExceptions(null)).toBeNull();
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
