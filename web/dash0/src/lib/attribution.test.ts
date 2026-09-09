import { afterEach, describe, expect, it } from "vitest";

import {
  captureLandingAttribution,
  clearSignupAttribution,
  parseAttribution,
  readSignupAttribution,
} from "@/lib/attribution";
import { DASH_BASE } from "@/lib/base-path";

const at = () => new Date("2026-09-07T10:00:00.000Z");

describe("parseAttribution", () => {
  it("returns null for an untagged URL", () => {
    expect(parseAttribution("", `${DASH_BASE}/`, at)).toBeNull();
    expect(parseAttribution("?returnTo=%2Forgs%2Facme", `${DASH_BASE}/login`, at)).toBeNull();
  });

  it("reads the marketing site's full tag set", () => {
    expect(
      parseAttribution(
        "?gclid=Cj0KCQjw&utm_source=google&utm_medium=cpc&utm_campaign=en-category&utm_term=uptime+monitor&utm_content=rsa-1",
        `${DASH_BASE}/`,
        at,
      ),
    ).toEqual({
      utmSource: "google",
      utmMedium: "cpc",
      utmCampaign: "en-category",
      utmTerm: "uptime monitor",
      utmContent: "rsa-1",
      clickIdKind: "gclid",
      clickId: "Cj0KCQjw",
      landingPath: `${DASH_BASE}/`,
      capturedAt: "2026-09-07T10:00:00.000Z",
    });
  });

  it("keeps a click id without any utm tag, and names its kind", () => {
    expect(parseAttribution("?msclkid=abc123", `${DASH_BASE}/`, at)).toMatchObject({
      clickIdKind: "msclkid",
      clickId: "abc123",
    });
  });

  it("ignores unknown click-id parameters", () => {
    expect(parseAttribution("?fbclid=abc123", `${DASH_BASE}/`, at)).toBeNull();
  });

  it("drops empty values and clips long ones", () => {
    const parsed = parseAttribution(`?utm_source=&utm_campaign=${"a".repeat(500)}`, `${DASH_BASE}/`, at);
    expect(parsed?.utmSource).toBeUndefined();
    expect(parsed?.utmCampaign).toHaveLength(200);
  });
});

describe("captureLandingAttribution", () => {
  afterEach(() => clearSignupAttribution());

  it("holds the last tagged landing and ignores untagged ones", () => {
    expect(readSignupAttribution()).toBeUndefined();

    captureLandingAttribution("?gclid=first&utm_campaign=a", `${DASH_BASE}/`);
    expect(readSignupAttribution()).toMatchObject({ clickId: "first", utmCampaign: "a" });

    captureLandingAttribution("?returnTo=%2Fx", `${DASH_BASE}/login`);
    expect(readSignupAttribution()).toMatchObject({ clickId: "first" });

    captureLandingAttribution("?gclid=second", `${DASH_BASE}/`);
    expect(readSignupAttribution()).toMatchObject({ clickId: "second" });
    expect(readSignupAttribution()?.utmCampaign).toBeUndefined();
  });

  it("is forgotten once cleared", () => {
    captureLandingAttribution("?gclid=first", `${DASH_BASE}/`);
    clearSignupAttribution();
    expect(readSignupAttribution()).toBeUndefined();
  });
});
