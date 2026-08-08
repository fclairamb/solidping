import { describe, expect, it } from "vitest";

import { httpModule, type HttpState } from "./http";

// Base state for toConfig tests — a valid URL and the implicit ["200"]
// default, so each test only needs to override what it's exercising.
function baseState(overrides: Partial<HttpState> = {}): HttpState {
  return {
    url: "https://example.com",
    method: "GET",
    expectedStatusCodes: ["200"],
    username: "",
    password: "",
    secretHeaders: [],
    verifySsl: true,
    followRedirects: true,
    authDirty: false,
    headersDirty: false,
    ...overrides,
  };
}

describe("httpModule.fromConfig — expectedStatusCodes seeding", () => {
  it("defaults to [\"200\"] when neither status key is present", () => {
    const state = httpModule.fromConfig({ url: "https://example.com" });
    expect(state.expectedStatusCodes).toEqual(["200"]);
  });

  it("seeds from expectedStatusCodes when present, normalizing entries", () => {
    const state = httpModule.fromConfig({
      url: "https://example.com",
      expectedStatusCodes: ["201", "4xx"],
    });
    expect(state.expectedStatusCodes).toEqual(["201", "4XX"]);
  });

  it("de-dupes the seeded list", () => {
    const state = httpModule.fromConfig({
      url: "https://example.com",
      expectedStatusCodes: ["200", "200", "4xx", "4XX"],
    });
    expect(state.expectedStatusCodes).toEqual(["200", "4XX"]);
  });

  it("falls back to the legacy expectedStatus int as a single chip", () => {
    const state = httpModule.fromConfig({
      url: "https://example.com",
      expectedStatus: 201,
    });
    expect(state.expectedStatusCodes).toEqual(["201"]);
  });

  it("prefers expectedStatusCodes over the legacy expectedStatus when both are present", () => {
    const state = httpModule.fromConfig({
      url: "https://example.com",
      expectedStatus: 500,
      expectedStatusCodes: ["200"],
    });
    expect(state.expectedStatusCodes).toEqual(["200"]);
  });

  it("ignores an empty expectedStatusCodes list and falls through to legacy/default", () => {
    const withLegacy = httpModule.fromConfig({
      url: "https://example.com",
      expectedStatusCodes: [],
      expectedStatus: 301,
    });
    expect(withLegacy.expectedStatusCodes).toEqual(["301"]);

    const withoutLegacy = httpModule.fromConfig({
      url: "https://example.com",
      expectedStatusCodes: [],
    });
    expect(withoutLegacy.expectedStatusCodes).toEqual(["200"]);
  });
});

describe("httpModule.toConfig — expectedStatusCodes serialization", () => {
  it("omits both status keys for the default [\"200\"]", () => {
    const { config } = httpModule.toConfig(baseState());
    expect(config).not.toHaveProperty("expectedStatusCodes");
    expect(config).not.toHaveProperty("expectedStatus");
  });

  it("omits both status keys for an empty list", () => {
    const { config } = httpModule.toConfig(
      baseState({ expectedStatusCodes: [] }),
    );
    expect(config).not.toHaveProperty("expectedStatusCodes");
    expect(config).not.toHaveProperty("expectedStatus");
  });

  it("writes expectedStatusCodes for a non-default list, never the legacy key", () => {
    const { config } = httpModule.toConfig(
      baseState({ expectedStatusCodes: ["200", "4XX"] }),
    );
    expect(config.expectedStatusCodes).toEqual(["200", "4XX"]);
    expect(config).not.toHaveProperty("expectedStatus");
  });

  it("round-trips a legacy expectedStatus check to expectedStatusCodes on next save", () => {
    const state = httpModule.fromConfig({
      url: "https://example.com",
      expectedStatus: 201,
    });
    const { config } = httpModule.toConfig(state);
    expect(config.expectedStatusCodes).toEqual(["201"]);
    expect(config).not.toHaveProperty("expectedStatus");
  });

  it("flags an invalid pattern with a field error without dropping it from the config", () => {
    const { config, errors } = httpModule.toConfig(
      baseState({ expectedStatusCodes: ["200", "6XX"] }),
    );
    expect(config.expectedStatusCodes).toEqual(["200", "6XX"]);
    const fieldError = errors.find((e) => e.name === "expectedStatusCodes");
    expect(fieldError).toBeDefined();
    expect(fieldError?.message).toContain("6XX");
  });

  it("still requires the URL", () => {
    const { errors } = httpModule.toConfig(baseState({ url: "" }));
    expect(errors.some((e) => e.name === "url")).toBe(true);
  });

  it("has no errors for a valid non-default list", () => {
    const { errors } = httpModule.toConfig(
      baseState({ expectedStatusCodes: ["200", "4XX", "500"] }),
    );
    expect(errors).toEqual([]);
  });
});

describe("httpModule — verifySsl / followRedirects round-trip", () => {
  it("fromConfig defaults both to true when absent", () => {
    const state = httpModule.fromConfig({ url: "https://example.com" });
    expect(state.verifySsl).toBe(true);
    expect(state.followRedirects).toBe(true);
  });

  it("fromConfig reads an explicit false", () => {
    const state = httpModule.fromConfig({
      url: "https://example.com",
      verifySsl: false,
      followRedirects: false,
    });
    expect(state.verifySsl).toBe(false);
    expect(state.followRedirects).toBe(false);
  });

  it("toConfig omits both keys at the default (true)", () => {
    const { config } = httpModule.toConfig(baseState());
    expect(config).not.toHaveProperty("verifySsl");
    expect(config).not.toHaveProperty("followRedirects");
  });

  it("toConfig writes false explicitly, never true", () => {
    const { config } = httpModule.toConfig(
      baseState({ verifySsl: false, followRedirects: false }),
    );
    expect(config.verifySsl).toBe(false);
    expect(config.followRedirects).toBe(false);
  });
});
