import { describe, expect, it } from "vitest";

import { dockerModule } from "./infra";

describe("dockerModule — daemon mode", () => {
  it("saves with both container fields empty (no validation error)", () => {
    const state = dockerModule.fromConfig({});
    const { config, errors } = dockerModule.toConfig(state);
    expect(errors).toEqual([]);
    expect(config.containerName).toBeUndefined();
    expect(config.containerId).toBeUndefined();
  });

  it("still sends the container name when set", () => {
    const state = dockerModule.fromConfig({ containerName: "postgres" });
    const { config, errors } = dockerModule.toConfig(state);
    expect(errors).toEqual([]);
    expect(config.containerName).toBe("postgres");
  });
});
