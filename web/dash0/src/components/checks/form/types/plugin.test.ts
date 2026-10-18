import { describe, expect, it } from "vitest";
import { moduleFor, checkTypeRegistry } from "./index";
import { assembleSubmittedConfig, isPluginCheckType } from "./common";
import { pluginErrors, type PluginState } from "./plugin";

const schema = {
  type: "object",
  properties: { host: { type: "string", minLength: 1 }, port: { type: "integer", minimum: 1 } },
  required: ["host"],
  additionalProperties: false,
};

describe("plugin check types", () => {
  it("falls back to the JSON editor for an unknown type", () => {
    const mod = moduleFor("x-gopher");
    expect(mod.types).toEqual(["x-gopher"]);
    expect(moduleFor("x-gopher")).toBe(mod);
    expect(moduleFor("http")).toBe(checkTypeRegistry.http);
    expect(isPluginCheckType("x-gopher")).toBe(true);
    expect(isPluginCheckType("http")).toBe(false);
  });

  it("round-trips a config through the editor text, minus shared keys", () => {
    const mod = moduleFor("x-gopher");
    const state = mod.fromConfig({ host: "gopher.acme.com", port: 70, timeout: "10s" }) as PluginState;
    expect(JSON.parse(state.text)).toEqual({ host: "gopher.acme.com", port: 70 });
    expect(mod.toConfig(state)).toEqual({ config: { host: "gopher.acme.com", port: 70 }, errors: [] });
  });

  it("reports invalid JSON and schema errors", () => {
    const broken = pluginErrors({ type: "x-gopher", text: "{ nope" });
    expect(broken.errors).toHaveLength(1);
    expect(broken.errors[0].name).toBe("config");

    const notObject = pluginErrors({ type: "x-gopher", text: "[1]" });
    expect(notObject.errors[0].name).toBe("config");

    const missing = pluginErrors({ type: "x-gopher", text: '{"port": 70}', schema });
    expect(missing.errors).toEqual([{ name: "host", message: "host: is required" }]);
  });

  it("never resurrects a key deleted in the editor", () => {
    const mod = moduleFor("x-gopher");
    const out = assembleSubmittedConfig({
      initialConfig: { host: "a", contains: "old" },
      ownedKeys: mod.ownedKeys,
      moduleConfig: { host: "b" },
      sharedConfig: { timeout: "5s" },
    });
    expect(out).toEqual({ host: "b", timeout: "5s" });
  });
});
