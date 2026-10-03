import { describe, expect, it } from "vitest";
import { addName, componentNamesOf, healthModule, warnOnlyNames } from "./health";

describe("healthModule", () => {
  it("round-trips a stored config", () => {
    const stored = {
      url: "https://app.acme.com/health",
      format: "spring",
      maxAge: "30m",
      ignore: ["Cache", "Queue"],
      components: { Mail: { onFailed: "warning" } },
    };
    const { config, errors } = healthModule.toConfig(healthModule.fromConfig(stored));
    expect(errors).toEqual([]);
    expect(config).toEqual(stored);
  });

  it("keeps the defaults implicit", () => {
    const { config, errors } = healthModule.toConfig(healthModule.fromConfig({ url: "https://app.acme.com/health" }));
    expect(errors).toEqual([]);
    expect(config).toEqual({ url: "https://app.acme.com/health" });
  });

  it("never writes http assertions or status expectations", () => {
    const state = healthModule.fromConfig({
      url: "https://app.acme.com/health",
      expectedStatusCodes: ["2XX"],
      jsonPathAssertions: { type: "assertion", path: "$.a", operator: "exists" },
    });
    const { config } = healthModule.toConfig(state);
    expect(config).not.toHaveProperty("expectedStatusCodes");
    expect(config).not.toHaveProperty("jsonPathAssertions");
  });

  it("accepts the snake_case aliases on read", () => {
    const state = healthModule.fromConfig({ url: "https://a.acme.com", max_age: "5m", components: { A: { on_failed: "warning" } } });
    expect(state.maxAge).toBe("5m");
    expect(state.warnOnly).toBe("A");
  });

  it("flags an invalid max age and a missing URL", () => {
    const { errors } = healthModule.toConfig(healthModule.fromConfig({ maxAge: "soon" }));
    expect(errors.map((e) => e.name).sort()).toEqual(["maxAge", "url"]);
  });

  it("allows 0 to disable the staleness rule", () => {
    const { config, errors } = healthModule.toConfig(healthModule.fromConfig({ url: "https://a.acme.com", maxAge: "0" }));
    expect(errors).toEqual([]);
    expect(config.maxAge).toBe("0");
  });

  it("declares every key toConfig writes", () => {
    const { config } = healthModule.toConfig(
      healthModule.fromConfig({
        url: "https://a.acme.com",
        method: "POST",
        format: "aspnet",
        maxAge: "1h",
        ignore: ["x"],
        components: { y: { onFailed: "warning" } },
        followRedirects: false,
        verifySsl: false,
      }),
    );
    for (const key of Object.keys(config)) {
      expect(healthModule.ownedKeys).toContain(key);
    }
  });
});

describe("helpers", () => {
  it("lists the warn-only names", () => {
    expect(warnOnlyNames({ A: { onFailed: "warning" }, B: { onFailed: "down" }, C: {} })).toEqual(["A"]);
    expect(warnOnlyNames(undefined)).toEqual([]);
  });

  it("reads component names from an output", () => {
    expect(componentNamesOf({ components: [{ name: "db" }, { name: "cache" }, {}] })).toEqual(["db", "cache"]);
    expect(componentNamesOf(undefined)).toEqual([]);
  });

  it("adds a name once", () => {
    expect(addName("a", "b")).toBe("a\nb");
    expect(addName("a\nb", "b")).toBe("a\nb");
  });
});
