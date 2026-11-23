import { describe, expect, it } from "vitest";

import { unixSocketModule } from "./unix-socket";
import { assembleSubmittedConfig, type CheckConfig } from "./common";

function roundTrip(stored: CheckConfig): CheckConfig {
  return unixSocketModule.toConfig(unixSocketModule.fromConfig(stored)).config;
}

describe("unixSocketModule", () => {
  it("round-trips connect-only", () => {
    const stored = { path: "/run/app/app.sock" };
    expect(unixSocketModule.fromConfig(stored).mode).toBe("connect");
    expect(roundTrip(stored)).toEqual(stored);
  });

  it("round-trips raw mode", () => {
    const stored = {
      path: "/run/app/app.sock",
      send_data: String.raw`PING\r\n`,
      send_encoding: "escaped",
      expect_data: "PONG",
    };
    expect(unixSocketModule.fromConfig(stored).mode).toBe("raw");
    expect(roundTrip(stored)).toEqual(stored);
  });

  it("round-trips HTTP mode", () => {
    const stored = {
      path: "/run/app/app.sock",
      http_path: "/healthcheck",
      expected_status: 204,
      body_expect: "ok",
    };
    expect(unixSocketModule.fromConfig(stored).mode).toBe("http");
    expect(roundTrip(stored)).toEqual(stored);
  });

  it("drops the other mode's keys when the mode switches", () => {
    const stored = {
      path: "/run/app/app.sock",
      send_data: "PING",
      expect_data: "PONG",
    };
    const state = { ...unixSocketModule.fromConfig(stored), mode: "http" as const, httpPath: "/health" };
    const submitted = assembleSubmittedConfig({
      initialConfig: stored,
      ownedKeys: unixSocketModule.ownedKeys,
      moduleConfig: unixSocketModule.toConfig(state).config,
    });
    expect(submitted).toEqual({ path: "/run/app/app.sock", http_path: "/health" });
  });

  it("requires an absolute path", () => {
    const empty = unixSocketModule.toConfig(unixSocketModule.fromConfig({}));
    expect(empty.errors.map((e) => e.name)).toEqual(["path"]);

    const relative = unixSocketModule.toConfig(
      unixSocketModule.fromConfig({ path: "run/app.sock" }),
    );
    expect(relative.errors.map((e) => e.name)).toEqual(["path"]);
  });

  it("declares every key it writes", () => {
    const state = {
      ...unixSocketModule.fromConfig({}),
      path: "/a.sock",
      mode: "http" as const,
      httpPath: "/x",
      expectedStatus: "200",
      bodyExpect: "ok",
    };
    for (const key of Object.keys(unixSocketModule.toConfig(state).config)) {
      expect(unixSocketModule.ownedKeys).toContain(key);
    }
  });
});
