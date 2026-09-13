import { describe, expect, it } from "vitest";

import { jsModule } from "./misc";
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
