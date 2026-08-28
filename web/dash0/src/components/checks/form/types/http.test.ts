import { describe, expect, it } from "vitest";

import { httpModule, type HttpState } from "./http";
import type { AssertionNode } from "@/components/checks/json-assertion-editor";

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
    captureFailureResponse: false,
    authDirty: false,
    headersDirty: false,
    jsonPathAssertions: null,
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

describe("httpModule — captureFailureResponse round-trip", () => {
  it("fromConfig defaults to false when absent", () => {
    const state = httpModule.fromConfig({ url: "https://acme.com" });
    expect(state.captureFailureResponse).toBe(false);
  });

  it("fromConfig reads the canonical snake_case key", () => {
    const state = httpModule.fromConfig({
      url: "https://acme.com",
      capture_failure_response: true,
    });
    expect(state.captureFailureResponse).toBe(true);
  });

  it("fromConfig accepts the camelCase alias the server also tolerates", () => {
    const state = httpModule.fromConfig({
      url: "https://acme.com",
      captureFailureResponse: true,
    });
    expect(state.captureFailureResponse).toBe(true);
  });

  it("toConfig omits the key at the default (false)", () => {
    const { config } = httpModule.toConfig(baseState());
    expect(config).not.toHaveProperty("capture_failure_response");
    expect(config).not.toHaveProperty("captureFailureResponse");
  });

  it("toConfig writes only the canonical snake_case key when enabled", () => {
    const { config } = httpModule.toConfig(
      baseState({ captureFailureResponse: true }),
    );
    expect(config.capture_failure_response).toBe(true);
    expect(config).not.toHaveProperty("captureFailureResponse");
  });

  it("round-trips through toConfig -> fromConfig", () => {
    const { config } = httpModule.toConfig(
      baseState({ captureFailureResponse: true }),
    );
    expect(httpModule.fromConfig(config).captureFailureResponse).toBe(true);
  });
});

describe("httpModule — jsonPathAssertions round-trip", () => {
  const leaf: AssertionNode = {
    type: "assertion",
    path: "$.status",
    operator: "eq",
    value: "ok",
  };
  const group: AssertionNode = {
    type: "and",
    children: [
      leaf,
      { type: "assertion", path: "$.uptime", operator: "gt", value: "0" },
    ],
  };

  it("fromConfig defaults to null when absent", () => {
    const state = httpModule.fromConfig({ url: "https://example.com" });
    expect(state.jsonPathAssertions).toBeNull();
  });

  it("fromConfig seeds from the canonical camelCase key", () => {
    const state = httpModule.fromConfig({
      url: "https://example.com",
      jsonPathAssertions: leaf,
    });
    expect(state.jsonPathAssertions).toEqual(leaf);
  });

  it("fromConfig accepts the snake_case alias the server also resolves", () => {
    const state = httpModule.fromConfig({
      url: "https://example.com",
      json_path_assertions: leaf,
    });
    expect(state.jsonPathAssertions).toEqual(leaf);
  });

  it("fromConfig prefers the camelCase key when both are present", () => {
    const state = httpModule.fromConfig({
      url: "https://example.com",
      jsonPathAssertions: leaf,
      json_path_assertions: group,
    });
    expect(state.jsonPathAssertions).toEqual(leaf);
  });

  it("fromConfig seeds a group tree unchanged", () => {
    const state = httpModule.fromConfig({
      url: "https://example.com",
      jsonPathAssertions: group,
    });
    expect(state.jsonPathAssertions).toEqual(group);
  });

  it("toConfig omits the key when null (default)", () => {
    const { config } = httpModule.toConfig(baseState());
    expect(config).not.toHaveProperty("jsonPathAssertions");
    expect(config).not.toHaveProperty("json_path_assertions");
  });

  it("toConfig writes the canonical camelCase key when present", () => {
    const { config } = httpModule.toConfig(
      baseState({ jsonPathAssertions: leaf }),
    );
    expect(config.jsonPathAssertions).toEqual(leaf);
    expect(config).not.toHaveProperty("json_path_assertions");
  });

  it("round-trips a leaf assertion unchanged through an edit-and-save with no changes", () => {
    const state = httpModule.fromConfig({
      url: "https://example.com",
      jsonPathAssertions: leaf,
    });
    const { config } = httpModule.toConfig(state);
    expect(httpModule.fromConfig(config).jsonPathAssertions).toEqual(leaf);
  });

  it("round-trips a group tree unchanged through an edit-and-save with no changes", () => {
    const state = httpModule.fromConfig({
      url: "https://example.com",
      jsonPathAssertions: group,
    });
    const { config } = httpModule.toConfig(state);
    expect(httpModule.fromConfig(config).jsonPathAssertions).toEqual(group);
  });

  it("clearing the tree (set back to null) omits the key so it clears on save", () => {
    const seeded = httpModule.fromConfig({
      url: "https://example.com",
      jsonPathAssertions: leaf,
    });
    const cleared: HttpState = { ...seeded, jsonPathAssertions: null };
    const { config } = httpModule.toConfig(cleared);
    expect(config).not.toHaveProperty("jsonPathAssertions");
    expect(config).not.toHaveProperty("json_path_assertions");
    // And re-seeding from the cleared config finds nothing to show.
    expect(httpModule.fromConfig(config).jsonPathAssertions).toBeNull();
  });

  it("does not affect other config keys' serialization", () => {
    const { config } = httpModule.toConfig(
      baseState({ jsonPathAssertions: leaf, verifySsl: false }),
    );
    expect(config.jsonPathAssertions).toEqual(leaf);
    expect(config.verifySsl).toBe(false);
  });
});
