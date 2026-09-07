import { afterEach, describe, expect, it } from "vitest";

import {
  captureLandingAttribution,
  clearSignupAttribution,
  parseAttribution,
  readSignupAttribution,
} from "@/lib/attribution";

const at = () => new Date("2026-09-07T10:00:00.000Z");

describe("parseAttribution", () => {
  it("returns null for an untagged URL", () => {
    expect(parseAttribution("", "/dash0/", at)).toBeNull();
    expect(parseAttribution("?returnTo=%2Forgs%2Facme", "/dash0/login", at)).toBeNull();
  });

  it("reads the marketing site's full tag set", () => {
    expect(
      parseAttribution(
        "?gclid=Cj0KCQjw&utm_source=google&utm_medium=cpc&utm_campaign=en-category&utm_term=uptime+monitor&utm_content=rsa-1",
        "/dash0/",
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
      landingPath: "/dash0/",
      capturedAt: "2026-09-07T10:00:00.000Z",
    });
  });

  it("keeps a click id without any utm tag, and names its kind", () => {
    expect(parseAttribution("?msclkid=abc123", "/dash0/", at)).toMatchObject({
      clickIdKind: "msclkid",
      clickId: "abc123",
    });
  });

  it("ignores unknown click-id parameters", () => {
    expect(parseAttribution("?fbclid=abc123", "/dash0/", at)).toBeNull();
  });

  it("drops empty values and clips long ones", () => {
    const parsed = parseAttribution(`?utm_source=&utm_campaign=${"a".repeat(500)}`, "/dash0/", at);
    expect(parsed?.utmSource).toBeUndefined();
    expect(parsed?.utmCampaign).toHaveLength(200);
  });
});

describe("captureLandingAttribution", () => {
  afterEach(() => clearSignupAttribution());

  it("holds the last tagged landing and ignores untagged ones", () => {
    expect(readSignupAttribution()).toBeUndefined();

    captureLandingAttribution("?gclid=first&utm_campaign=a", "/dash0/");
    expect(readSignupAttribution()).toMatchObject({ clickId: "first", utmCampaign: "a" });

    captureLandingAttribution("?returnTo=%2Fx", "/dash0/login");
    expect(readSignupAttribution()).toMatchObject({ clickId: "first" });

    captureLandingAttribution("?gclid=second", "/dash0/");
    expect(readSignupAttribution()).toMatchObject({ clickId: "second" });
    expect(readSignupAttribution()?.utmCampaign).toBeUndefined();
  });

  it("is forgotten once cleared", () => {
    captureLandingAttribution("?gclid=first", "/dash0/");
    clearSignupAttribution();
    expect(readSignupAttribution()).toBeUndefined();
  });
});
