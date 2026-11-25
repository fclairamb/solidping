import { describe, expect, it } from "vitest";

import { checkTargetOf } from "@/lib/check-target";

describe("checkTargetOf", () => {
  it("shows the port of a host/port check (#998)", () => {
    expect(checkTargetOf({ host: "localhost", port: 5432 })).toBe("localhost:5432");
    expect(checkTargetOf({ host: "db.example.com", port: "6379" })).toBe("db.example.com:6379");
  });

  it("prefers the url", () => {
    expect(checkTargetOf({ url: "https://example.com", host: "x", port: 1 })).toBe(
      "https://example.com",
    );
  });

  it("shows the host alone without a port", () => {
    expect(checkTargetOf({ host: "example.com" })).toBe("example.com");
    expect(checkTargetOf({ host: "example.com", port: 0 })).toBe("example.com");
    expect(checkTargetOf({ host: "example.com", port: "" })).toBe("example.com");
  });

  it("does not append a port the host already carries", () => {
    expect(checkTargetOf({ host: "example.com:8080", port: 8080 })).toBe("example.com:8080");
    expect(checkTargetOf({ host: "[::1]:5432", port: 5432 })).toBe("[::1]:5432");
  });

  it("brackets a bare IPv6 host", () => {
    expect(checkTargetOf({ host: "::1", port: 5432 })).toBe("[::1]:5432");
    expect(checkTargetOf({ host: "[::1]", port: 5432 })).toBe("[::1]:5432");
  });

  it("shows the Unix socket path of a socket check (#1042)", () => {
    expect(checkTargetOf({ socket: "/var/run/postgresql/.s.PGSQL.5432" })).toBe(
      "/var/run/postgresql/.s.PGSQL.5432",
    );
    expect(checkTargetOf({ socket: "/run/redis.sock", host: "", port: 6379 })).toBe(
      "/run/redis.sock",
    );
  });

  it("prefers the socket over the url of an http check (#1042)", () => {
    expect(
      checkTargetOf({ url: "http://localhost/health", unix_socket: "/run/app.sock" }),
    ).toBe("/run/app.sock");
  });

  it("has no target without url or host", () => {
    expect(checkTargetOf(undefined)).toBeUndefined();
    expect(checkTargetOf({ token: "abc" })).toBeUndefined();
  });
});
