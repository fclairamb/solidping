import { describe, expect, it } from "vitest";

import { httpModule, type HttpState } from "./http";
import { checkTypeRegistry, type CheckTypeModule } from "./index";
import {
  assembleSubmittedConfig,
  passthroughConfigFor,
  SHARED_FORM_CONFIG_KEYS,
  type CheckConfig,
} from "./common";
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
    body: "",
    headers: [],
    ...overrides,
  };
}

// The secret keys the check-types metadata advertises for `http`
// (checkhttp.SecretFields). Hard-coded here only because the unit test has no
// API; the form itself reads them from the server.
const HTTP_SECRET_FIELDS = ["basicAuth", "password", "secretHeaders"];

// saveUntouched reproduces exactly what the shared form submits when a check is
// opened and saved without touching a field: seed the module from the stored
// config, serialize it back, and layer the passthrough the form assembles.
function saveUntouched(stored: CheckConfig): CheckConfig {
  const state = httpModule.fromConfig(stored);
  const { config } = httpModule.toConfig(state);
  return assembleSubmittedConfig({
    initialConfig: stored,
    ownedKeys: httpModule.ownedKeys,
    secretFields: HTTP_SECRET_FIELDS,
    moduleConfig: config,
  });
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

// ---------------------------------------------------------------------------
// Spec 2026-09-11-01: the form must not delete config keys it does not model.
//
// Every assertion below is on the config the form would SUBMIT — the same
// object `check-form.tsx` builds and PATCHes — because the bug is invisible in
// client state: the module's own round-trip has always looked fine.
// ---------------------------------------------------------------------------

describe("unmodeled config keys survive an untouched save", () => {
  // The reported case, verbatim in shape: a POST to a token endpoint whose
  // body, headers and snake-spelled expected status the form has never shown.
  const stored: CheckConfig = {
    url: "https://sso.acme.com/realms/acme/protocol/openid-connect/token",
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded" },
    body: "grant_type=password&client_id=admin-cli&scope=openid",
    expected_status: 200,
    followRedirects: false,
    body_expect: "access_token",
    body_pattern: "\"expires_in\":\\s*\\d+",
    headers_pattern: { "content-type": "^application/json" },
    jsonPathAssertions: {
      type: "assertion",
      path: "$.token_type",
      operator: "eq",
      value: "Bearer",
    } as unknown as AssertionNode,
  };

  it("keeps the keys the form does not model, byte-for-byte", () => {
    const submitted = saveUntouched(stored);
    expect(submitted.body_expect).toBe(stored.body_expect);
    expect(submitted.body_pattern).toBe(stored.body_pattern);
    expect(submitted.headers_pattern).toEqual(stored.headers_pattern);
  });

  it("keeps the request body and plain headers (now modelled)", () => {
    const submitted = saveUntouched(stored);
    expect(submitted.body).toBe(stored.body);
    expect(submitted.headers).toEqual(stored.headers);
  });

  it("keeps the effective expected status from the snake-case spelling", () => {
    const state = httpModule.fromConfig(stored);
    expect(state.expectedStatusCodes).toEqual(["200"]);
    const submitted = saveUntouched(stored);
    // 200 is the implicit default, so neither key needs to be written — what
    // must never happen is a DIFFERENT effective status.
    const effective =
      submitted.expectedStatusCodes ??
      submitted.expected_status_codes ??
      submitted.expectedStatus ??
      submitted.expected_status ??
      ["200"];
    expect(effective).toEqual(["200"]);
  });

  it("preserves a non-default snake expected_status as a real chip", () => {
    const withNonDefault = { ...stored, expected_status: 201 };
    const state = httpModule.fromConfig(withNonDefault);
    expect(state.expectedStatusCodes).toEqual(["201"]);
    const submitted = saveUntouched(withNonDefault);
    expect(submitted.expectedStatusCodes).toEqual(["201"]);
    // The legacy spelling is owned, so it is not resurrected alongside it.
    expect(submitted).not.toHaveProperty("expected_status");
  });

  it("does not resurrect an owned key the user cleared", () => {
    // jsonPathAssertions IS modelled: clearing it in the UI must still delete
    // it, which is exactly what the passthrough must not undo.
    const state = httpModule.fromConfig(stored);
    const { config } = httpModule.toConfig({
      ...state,
      jsonPathAssertions: null,
    });
    const submitted = assembleSubmittedConfig({
      initialConfig: stored,
      ownedKeys: httpModule.ownedKeys,
      secretFields: HTTP_SECRET_FIELDS,
      moduleConfig: config,
    });
    expect(submitted).not.toHaveProperty("jsonPathAssertions");
    expect(submitted).not.toHaveProperty("json_path_assertions");
    // …while the unmodeled keys are still there.
    expect(submitted.body_expect).toBe(stored.body_expect);
  });

  it("clears plain headers when the editor is emptied", () => {
    const state = httpModule.fromConfig(stored);
    const { config } = httpModule.toConfig({ ...state, headers: [] });
    const submitted = assembleSubmittedConfig({
      initialConfig: stored,
      ownedKeys: httpModule.ownedKeys,
      secretFields: HTTP_SECRET_FIELDS,
      moduleConfig: config,
    });
    expect(submitted).not.toHaveProperty("headers");
  });

  it("never carries a secret field through, even if one is present", () => {
    // Secrets are stripped from every read today, so this cannot normally
    // happen — but if a deployment ever did return them, the passthrough must
    // not be what re-sends a credential the user never touched. The module's
    // own dirty flags decide that; here both sections are untouched, so the
    // ONLY way a secret could reach the payload is the passthrough.
    const leaky: CheckConfig = {
      ...stored,
      basicAuth: "user:hunter2",
      password: "hunter2",
      secretHeaders: { "x-api-key": "shhh" },
    };
    const state = httpModule.fromConfig(leaky);
    const { config } = httpModule.toConfig({
      ...state,
      authDirty: false,
      headersDirty: false,
    });
    expect(config).not.toHaveProperty("password");
    const submitted = assembleSubmittedConfig({
      initialConfig: leaky,
      ownedKeys: httpModule.ownedKeys,
      secretFields: HTTP_SECRET_FIELDS,
      moduleConfig: config,
    });
    expect(submitted).not.toHaveProperty("basicAuth");
    expect(submitted).not.toHaveProperty("password");
    expect(submitted).not.toHaveProperty("secretHeaders");
    // Positive control: the same assembly DID carry the non-secret keys.
    expect(submitted.body_expect).toBe(stored.body_expect);
  });

  it("excludes secret fields even when the module does not own them", () => {
    // A module that models none of its type's secrets must still not have them
    // resurrected by the passthrough — that is what `secretFields` is for, and
    // it is why the list comes from the server rather than from ownedKeys.
    const submitted = assembleSubmittedConfig({
      initialConfig: { keepMe: "yes", apiToken: "shhh" },
      ownedKeys: [],
      secretFields: ["apiToken"],
      moduleConfig: {},
    });
    expect(submitted).toEqual({ keepMe: "yes" });
  });

  it("never carries a shared-form key through", () => {
    // timeout / tunnelCheckUid / ipVersion are owned by check-form.tsx itself;
    // the passthrough must leave them to it or clearing the timeout input
    // would stop deleting the key.
    const withShared: CheckConfig = {
      ...stored,
      timeout: "15s",
      tunnelCheckUid: "some-uid",
      ipVersion: "ipv6",
    };
    const submitted = saveUntouched(withShared);
    for (const key of SHARED_FORM_CONFIG_KEYS) {
      expect(submitted).not.toHaveProperty(key);
    }
  });

  it("is a no-op in create mode (no initial config)", () => {
    const { config } = httpModule.toConfig(baseState({ url: "https://a.dev" }));
    const submitted = assembleSubmittedConfig({
      ownedKeys: httpModule.ownedKeys,
      secretFields: HTTP_SECRET_FIELDS,
      moduleConfig: config,
    });
    expect(submitted).toEqual({ url: "https://a.dev" });
  });

  it("cannot smuggle an http-only key into another type's payload", () => {
    // The real gate: the form keeps the passthrough source tagged with the type
    // it came from and `passthroughConfigFor` refuses it once the active type
    // has moved on. Exercise the MISMATCH — an http-shaped source while `tcp`
    // is selected — so deleting the gate fails this test.
    const source = { type: "http" as const, config: stored };
    const tcp = checkTypeRegistry.tcp;
    const { config } = tcp.toConfig(tcp.fromConfig({ host: "a.dev", port: 22 }));
    const submitted = assembleSubmittedConfig({
      initialConfig: passthroughConfigFor(source, "tcp"),
      ownedKeys: tcp.ownedKeys,
      moduleConfig: config,
    });
    expect(submitted).not.toHaveProperty("body");
    expect(submitted).not.toHaveProperty("body_expect");
    expect(submitted).not.toHaveProperty("headers_pattern");
    expect(submitted).toEqual({ host: "a.dev", port: 22 });
  });

  it("still passes through while the source matches the active type", () => {
    // Negative control for the gate above: if `passthroughConfigFor` simply
    // returned undefined always, the test above would pass and the whole
    // feature would be dead. This is what proves it does not.
    const source = { type: "http" as const, config: stored };
    expect(passthroughConfigFor(source, "http")).toBe(stored);
    expect(passthroughConfigFor(source, "tcp")).toBeUndefined();
    expect(passthroughConfigFor(undefined, "http")).toBeUndefined();

    const { config } = httpModule.toConfig(httpModule.fromConfig(stored));
    const submitted = assembleSubmittedConfig({
      initialConfig: passthroughConfigFor(source, "http"),
      ownedKeys: httpModule.ownedKeys,
      secretFields: HTTP_SECRET_FIELDS,
      moduleConfig: config,
    });
    expect(submitted.body_expect).toBe(stored.body_expect);
  });
});

describe("the body editor's method gate does not destroy the body", () => {
  it("keeps a body set while the method is GET", () => {
    const state = httpModule.fromConfig({
      url: "https://example.com",
      method: "POST",
      body: "a=1",
    });
    const asGet: HttpState = { ...state, method: "GET" };
    // The editor is hidden for GET, but the value is still serialized, so
    // switching back to POST finds it intact.
    const { config } = httpModule.toConfig(asGet);
    expect(config.body).toBe("a=1");
    expect(httpModule.fromConfig(config).body).toBe("a=1");
  });
});

// undeclaredKeysFor returns the config keys a module's `toConfig` writes but
// does NOT list in `ownedKeys` — i.e. the keys the passthrough would resurrect
// after the user cleared them, which is the regression this spec exists to
// prevent ("add a modelled field, forget to declare it").
//
// The state is populated by driving `fromConfig` with a PROXY config that
// answers every property read with a plausible value. That is the whole point:
// deriving the seed from `ownedKeys` (or from the union of all modules'
// ownedKeys) can never surface a key nobody declares — the undeclared key is
// simply never seeded, `fromConfig` reads "", the guarded `if (state.x)` write
// never fires, and the check passes vacuously. A proxy answers for keys the
// test has never heard of, so it does not depend on the very list it audits.
//
// Several value shapes are tried because the seeding is type-dependent (arrays
// for expected_ips, maps for headers, "true" strings for switches); the union
// of every shape's writes is what gets audited.
function seedProxy(value: unknown): CheckConfig {
  return new Proxy({} as CheckConfig, {
    get: (_target, prop) => (typeof prop === "symbol" ? undefined : value),
    has: () => true,
    // Object.keys/entries on the config itself is never done by a module, but
    // keep the trap honest rather than throwing if that ever changes.
    ownKeys: () => [],
    getOwnPropertyDescriptor: () => undefined,
  });
}

function undeclaredKeysFor(mod: CheckTypeModule): string[] {
  const declared = new Set(mod.ownedKeys);
  const found = new Set<string>();
  const seeds: CheckConfig[] = [
    {},
    seedProxy("1"),
    seedProxy("true"),
    seedProxy(1),
    seedProxy(true),
    // `false` matters: several modules only write a key at its non-default
    // FALSE value (verifySsl, followRedirects), so no truthy shape reaches
    // those writes.
    seedProxy(false),
    seedProxy(["1"]),
    seedProxy({ "1": "1" }),
  ];
  for (const seed of seeds) {
    const { config } = mod.toConfig(mod.fromConfig(seed));
    for (const written of Object.keys(config)) {
      if (!declared.has(written)) found.add(written);
    }
  }
  return [...found].sort();
}

// ---------------------------------------------------------------------------
// The same failure mode as the spec, one layer down: a key declared in
// `ownedKeys` under BOTH spellings but READ under only one. The stored value
// never reaches the state, `toConfig` omits the key as "default", and because
// the key is owned the passthrough does not save it either — so an untouched
// UI save deletes it. Registry-wide, so it lives beside the guard below.
// ---------------------------------------------------------------------------
describe("a stored key is seeded from the spelling it is stored under", () => {
  it("http: snake verify_ssl / follow_redirects survive a save", () => {
    const stored: CheckConfig = {
      url: "https://acme.com/",
      verify_ssl: false,
      follow_redirects: false,
    };
    const state = httpModule.fromConfig(stored);
    // Before the fix both seeded `true` — the form showed TLS verification ON
    // for a check that had it off, and the save made that real.
    expect(state.verifySsl).toBe(false);
    expect(state.followRedirects).toBe(false);

    const { config } = httpModule.toConfig(state);
    const submitted = assembleSubmittedConfig({
      initialConfig: stored,
      ownedKeys: httpModule.ownedKeys,
      secretFields: HTTP_SECRET_FIELDS,
      moduleConfig: config,
    });
    // Re-emitted under the canonical spelling, and — the part that matters —
    // still OFF rather than silently back on.
    expect(submitted.verifySsl).toBe(false);
    expect(submitted.followRedirects).toBe(false);
    expect(httpModule.fromConfig(submitted).verifySsl).toBe(false);
    expect(httpModule.fromConfig(submitted).followRedirects).toBe(false);
  });

  it("http: an explicit camel false still round-trips", () => {
    const state = httpModule.fromConfig({
      url: "https://acme.com/",
      verifySsl: false,
      followRedirects: false,
    });
    expect(state.verifySsl).toBe(false);
    expect(state.followRedirects).toBe(false);
  });

  it("rabbitmq: a stored tls flag is not turned off by a save", () => {
    const mod = checkTypeRegistry.rabbitmq;
    const stored: CheckConfig = { host: "mq.acme.com", tls: true };
    const { config } = mod.toConfig(mod.fromConfig(stored));
    const submitted = assembleSubmittedConfig({
      initialConfig: stored,
      ownedKeys: mod.ownedKeys,
      moduleConfig: config,
    });
    expect(submitted.tls).toBe(true);
  });

  it("kafka: a stored saslUsername is not deleted by a save", () => {
    const mod = checkTypeRegistry.kafka;
    const stored: CheckConfig = {
      brokers: ["b1:9092"],
      topic: "events",
      saslUsername: "probe",
    };
    const { config } = mod.toConfig(mod.fromConfig(stored));
    const submitted = assembleSubmittedConfig({
      initialConfig: stored,
      // saslPassword is a declared secret: absent from the payload is what
      // makes the server preserve the stored one.
      secretFields: ["saslPassword"],
      ownedKeys: mod.ownedKeys,
      moduleConfig: config,
    });
    expect(submitted.saslUsername).toBe("probe");
    expect(submitted.saslMechanism).toBe("PLAIN");
    expect(submitted).not.toHaveProperty("saslPassword");
  });
});

describe("every registered module declares the config keys it writes", () => {
  const modules = new Map<string, CheckTypeModule>();
  for (const [type, mod] of Object.entries(checkTypeRegistry)) {
    if (!modules.has(mod.types.join(","))) modules.set(mod.types.join(","), mod);
    expect(mod.types).toContain(type);
  }

  for (const [name, mod] of modules) {
    it(`${name}: toConfig writes only declared keys`, () => {
      const declared = new Set(mod.ownedKeys);
      // ownedKeys must never claim a key the shared form owns, or the shared
      // field would be overwritten by a stale module value.
      for (const shared of SHARED_FORM_CONFIG_KEYS) {
        expect(declared.has(shared)).toBe(false);
      }
      expect(undeclaredKeysFor(mod)).toEqual([]);
    });
  }

  // POSITIVE CONTROL. A guard that cannot fail is worse than no guard: it
  // reads as coverage. This sabotages a real module by removing one key from
  // its declaration and asserts the guard names it — including `body`, the key
  // THIS spec added, which is declared by no other module and is written only
  // under `if (state.body)`. The union-seeded version of this test passed with
  // `body` removed; this one does not.
  it("actually catches an undeclared key (positive control)", () => {
    for (const drop of ["body", "headers", "url", "jsonPathAssertions"]) {
      const sabotaged: CheckTypeModule = {
        ...(httpModule as unknown as CheckTypeModule),
        ownedKeys: httpModule.ownedKeys.filter((k) => k !== drop),
      };
      expect(
        undeclaredKeysFor(sabotaged),
        `removing "${drop}" from ownedKeys must be caught`,
      ).toContain(drop);
    }
  });

  it("declares ownedKeys for every module that models any config", () => {
    // heartbeat and email model no config key at all — that is deliberate and
    // is what makes the passthrough preserve a heartbeat's public `token`.
    const noConfigModules = ["heartbeat", "email"];
    for (const [name, mod] of modules) {
      if (noConfigModules.includes(name)) {
        expect(mod.ownedKeys).toEqual([]);
        continue;
      }
      expect(mod.ownedKeys.length, `${name} declares no ownedKeys`).toBeGreaterThan(0);
    }
  });
});
