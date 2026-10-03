import { describe, expect, it } from "vitest";

import { jsModule, vncModule } from "./misc";
import { assembleSubmittedConfig, type CheckConfig } from "./common";
import { secretRowsBecameDirty } from "@/components/ui/secret-key-value-rows";

// The secret keys the check-types metadata advertises for `js`
// (checkjs.SecretFields). Hard-coded because the unit test has no API; the form
// itself reads them from the server.
const JS_SECRET_FIELDS = ["secrets"];

// saveUntouched reproduces exactly what the shared form submits when a check is
// opened and saved without touching a field.
function saveUntouched(stored: CheckConfig): CheckConfig {
  const state = jsModule.fromConfig(stored);
  const { config } = jsModule.toConfig(state);
  return assembleSubmittedConfig({
    initialConfig: stored,
    ownedKeys: jsModule.ownedKeys,
    secretFields: JS_SECRET_FIELDS,
    moduleConfig: config,
  });
}

describe("jsModule — the env / secrets split", () => {
  it("seeds env from the stored config and never seeds secrets", () => {
    const state = jsModule.fromConfig({
      script: "return {status:'up'}",
      env: { BASE_URL: "https://acme.com" },
      // A deployment that somehow returned this must still not re-send it.
      secrets: { PASSWORD: "leaked" },
    });
    expect(state.env).toEqual([{ key: "BASE_URL", value: "https://acme.com" }]);
    expect(state.secrets).toEqual([]);
    expect(state.secretsDirty).toBe(false);
  });

  it("an untouched save keeps env and omits secrets entirely", () => {
    const submitted = saveUntouched({
      script: "return {status:'up'}",
      env: { BASE_URL: "https://acme.com" },
    });
    // THE regression this guards: `secrets` present (even as {}) would clear
    // the stored credential on every unrelated edit.
    expect(submitted).not.toHaveProperty("secrets");
    expect(submitted.env).toEqual({ BASE_URL: "https://acme.com" });
    expect(submitted.script).toBe("return {status:'up'}");
  });

  it("a touched secrets section is submitted, and an emptied one clears", () => {
    const base = jsModule.fromConfig({ script: "x" });

    const typed = jsModule.toConfig({
      ...base,
      secrets: [{ key: "PASSWORD", value: "hunter2" }],
      secretsDirty: true,
    });
    expect(typed.config.secrets).toEqual({ PASSWORD: "hunter2" });

    const cleared = jsModule.toConfig({ ...base, secrets: [], secretsDirty: true });
    expect(cleared.config.secrets).toEqual({});
  });

  it("clearing the env editor omits the key, which is what clears it", () => {
    const state = jsModule.fromConfig({
      script: "x",
      env: { BASE_URL: "https://acme.com" },
    });
    const { config } = jsModule.toConfig({ ...state, env: [] });
    expect(config).not.toHaveProperty("env");
    // And `env` is an owned key, so the passthrough does not resurrect it.
    const submitted = assembleSubmittedConfig({
      initialConfig: { script: "x", env: { BASE_URL: "https://acme.com" } },
      ownedKeys: jsModule.ownedKeys,
      secretFields: JS_SECRET_FIELDS,
      moduleConfig: config,
    });
    expect(submitted).not.toHaveProperty("env");
  });

  it("a blank row writes no empty-named entry", () => {
    const base = jsModule.fromConfig({ script: "x" });
    const { config } = jsModule.toConfig({
      ...base,
      env: [{ key: "", value: "" }],
      secrets: [{ key: "", value: "" }],
      secretsDirty: true,
    });
    expect(config).not.toHaveProperty("env");
    expect(config.secrets).toEqual({});
  });
});

describe("secretRowsBecameDirty", () => {
  const row = (key: string) => ({ key, value: "v" });

  it("adding a blank row does NOT dirty the section", () => {
    // A stray click on "add" followed by a save must not clear stored values.
    expect(secretRowsBecameDirty([], [{ key: "", value: "" }])).toBe(false);
    expect(
      secretRowsBecameDirty([row("A")], [row("A"), { key: "", value: "" }]),
    ).toBe(false);
  });

  it("editing a row dirties the section", () => {
    expect(
      secretRowsBecameDirty(
        [{ key: "A", value: "" }],
        [{ key: "A", value: "x" }],
      ),
    ).toBe(true);
  });

  it("removing a row dirties the section", () => {
    expect(secretRowsBecameDirty([row("A"), row("B")], [row("A")])).toBe(true);
  });
});

describe("vncModule", () => {
  it("defaults requireAuth to on and always serializes it", () => {
    const state = vncModule.fromConfig({ host: "vnc.acme.com" });
    expect(state.requireAuth).toBe(true);
    const { config, errors } = vncModule.toConfig(state);
    expect(config).toEqual({ host: "vnc.acme.com", requireAuth: true });
    expect(errors).toEqual([]);
  });

  it("round-trips an explicit requireAuth false, port, password and screenshot", () => {
    const state = vncModule.fromConfig({
      host: "vnc.acme.com",
      port: 5901,
      requireAuth: false,
      password: "pw",
      screenshot: true,
    });
    expect(state.requireAuth).toBe(false);
    expect(vncModule.toConfig(state).config).toEqual({
      host: "vnc.acme.com",
      port: 5901,
      requireAuth: false,
      password: "pw",
      screenshot: true,
    });
  });

  it("flags a screenshot without a password on the password field", () => {
    const state = vncModule.fromConfig({ host: "vnc.acme.com", screenshot: true });
    const { errors } = vncModule.toConfig(state);
    expect(errors.map((e) => e.name)).toEqual(["password"]);
  });

  it("requires a host", () => {
    const { errors } = vncModule.toConfig(vncModule.fromConfig({}));
    expect(errors.map((e) => e.name)).toContain("host");
  });

  it("round-trips username and tlsVerify, and omits them by default", () => {
    const state = vncModule.fromConfig({
      host: "vnc.acme.com",
      password: "pw",
      username: "alice",
      tlsVerify: true,
    });
    expect(state.tlsVerify).toBe(true);
    expect(vncModule.toConfig(state).config).toEqual({
      host: "vnc.acme.com",
      password: "pw",
      username: "alice",
      tlsVerify: true,
      requireAuth: true,
    });
    const plain = vncModule.fromConfig({ host: "vnc.acme.com" });
    expect(plain.tlsVerify).toBe(false);
    expect(vncModule.toConfig(plain).config).not.toHaveProperty("tlsVerify");
  });

  it("flags a username without a password, and one over 63 bytes", () => {
    const noPw = vncModule.fromConfig({ host: "vnc.acme.com", username: "alice" });
    expect(vncModule.toConfig(noPw).errors.map((e) => e.name)).toEqual(["password"]);

    const ok = vncModule.fromConfig({ host: "h", password: "pw", username: "u".repeat(63) });
    expect(vncModule.toConfig(ok).errors).toEqual([]);

    const long = vncModule.fromConfig({ host: "h", password: "pw", username: "u".repeat(64) });
    expect(vncModule.toConfig(long).errors.map((e) => e.name)).toEqual(["username"]);
  });
});

describe("jsModule.fromSample", () => {
  it("seeds one empty secret row per declared key", () => {
    const state = jsModule.fromSample!({ script: "x", secrets: { REDIS_PASSWORD: "" } });
    expect(state.secrets).toEqual([{ key: "REDIS_PASSWORD", value: "" }]);
    expect(state.secretsDirty).toBe(false);
  });

  it("never carries a sample secret value", () => {
    const state = jsModule.fromSample!({ script: "x", secrets: { PASSWORD: "leak" } });
    expect(state.secrets).toEqual([{ key: "PASSWORD", value: "" }]);
  });

  it("fromConfig still seeds no secrets for an existing check", () => {
    expect(jsModule.fromConfig({ script: "x", secrets: { PASSWORD: "p" } }).secrets).toEqual([]);
  });
});

describe("jsModule — the ai block of an AI-authored check", () => {
  const stored: CheckConfig = {
    script: "return { status: 'up' };",
    ai: {
      prompt: "the acme dashboard shows a project",
      contract: ["GET / answers 200", "a project is listed"],
      model: "glm-5-3-flash",
      generated_at: "2026-10-03T22:55:33Z",
      repair: "auto",
    },
  };

  it("survives an unrelated edit untouched", () => {
    const { config } = jsModule.toConfig(jsModule.fromConfig(stored));
    expect(config.ai).toEqual(stored.ai);
  });

  it("carries an edited prompt and contract", () => {
    const state = jsModule.fromConfig(stored);
    expect(state.ai?.contract).toBe("GET / answers 200\na project is listed");
    const { config, errors } = jsModule.toConfig({
      ...state,
      ai: { ...state.ai!, prompt: "  new prompt ", contract: "first\n\n  second  \n" },
    });
    expect(errors).toEqual([]);
    expect(config.ai).toMatchObject({ prompt: "new prompt", contract: ["first", "second"], repair: "auto" });
  });

  it("rejects a prompt without a contract", () => {
    const state = jsModule.fromConfig(stored);
    const { errors } = jsModule.toConfig({ ...state, ai: { ...state.ai!, contract: "  \n" } });
    expect(errors.map((e) => e.name)).toContain("aiContract");
  });

  it("is absent for a hand-written script", () => {
    const state = jsModule.fromConfig({ script: "x" });
    expect(state.ai).toBeUndefined();
    expect(jsModule.toConfig(state).config).not.toHaveProperty("ai");
  });
});
