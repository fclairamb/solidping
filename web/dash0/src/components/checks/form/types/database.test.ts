import { describe, expect, it } from "vitest";

import { redisModule, sqlDatabaseModule } from "./database";

// Unix socket target of the redis and postgres forms (spec 2026-11-24-03).
describe("redis socket target", () => {
  it("writes socket and no host or port in socket mode", () => {
    const state = redisModule.fromConfig({ socket: "/run/redis/redis.sock" });
    expect(state.targetMode).toBe("socket");
    const { config, errors } = redisModule.toConfig(state);
    expect(config).toEqual({ socket: "/run/redis/redis.sock" });
    expect(errors).toEqual([]);
  });

  it("requires the socket path in socket mode, not the host", () => {
    const state = {
      ...redisModule.fromConfig({}),
      targetMode: "socket" as const,
    };
    const { errors } = redisModule.toConfig(state);
    expect(errors.map((e) => e.name)).toEqual(["socket"]);
  });

  it("keeps host and port in host mode", () => {
    const state = redisModule.fromConfig({
      host: "redis.acme.com",
      port: 6380,
    });
    expect(state.targetMode).toBe("host");
    expect(redisModule.toConfig(state).config).toEqual({
      host: "redis.acme.com",
      port: 6380,
    });
  });
});

describe("postgresql socket target", () => {
  it("round-trips the socket with its username", () => {
    const stored = {
      socket: "/var/run/postgresql/.s.PGSQL.5432",
      username: "monitor",
    };
    const state = sqlDatabaseModule.fromConfig(stored);
    expect(state.targetMode).toBe("socket");
    const { config, errors } = sqlDatabaseModule.toConfig(state);
    expect(config).toEqual(stored);
    expect(errors).toEqual([]);
  });

  it("drops a stale host when switched to socket mode", () => {
    const state = {
      ...sqlDatabaseModule.fromConfig({ host: "db.acme.com" }),
      targetMode: "socket" as const,
      socket: "/var/run/postgresql/.s.PGSQL.5432",
    };
    const { config } = sqlDatabaseModule.toConfig(state);
    expect(config.host).toBeUndefined();
    expect(config.socket).toBe("/var/run/postgresql/.s.PGSQL.5432");
  });
});
