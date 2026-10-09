import { describe, expect, it } from "vitest";

import { sslModule } from "./misc";
import { tcpModule } from "./network";
import {
  applyHostInput,
  portForTypeSwitch,
  splitHostPort,
  withDefaultPort,
} from "./ports";

describe("splitHostPort", () => {
  it("splits host:port", () => {
    expect(splitHostPort("example.com:22")).toEqual({
      host: "example.com",
      port: "22",
    });
  });
  it("splits an IPv4 host:port", () => {
    expect(splitHostPort("10.0.0.1:5432")).toEqual({
      host: "10.0.0.1",
      port: "5432",
    });
  });
  it("splits bracketed IPv6", () => {
    expect(splitHostPort("[2001:db8::1]:443")).toEqual({
      host: "2001:db8::1",
      port: "443",
    });
  });
  it("leaves a bare IPv6 address alone", () => {
    expect(splitHostPort("2001:db8::1")).toBeNull();
    expect(splitHostPort("::1")).toBeNull();
  });
  it("leaves URLs, plain hosts and 6-digit ports alone", () => {
    expect(splitHostPort("https://example.com:443")).toBeNull();
    expect(splitHostPort("example.com")).toBeNull();
    expect(splitHostPort("example.com:123456")).toBeNull();
    expect(splitHostPort("example.com:")).toBeNull();
  });
});

describe("applyHostInput", () => {
  it("fills host and port on a paste", () => {
    const next = applyHostInput({ host: "", port: "443" }, "example.com:22");
    expect(next).toEqual({ host: "example.com", port: "22" });
  });
  it("keeps the port on a plain host edit", () => {
    expect(applyHostInput({ host: "", port: "443" }, "example.com")).toEqual({
      host: "example.com",
      port: "443",
    });
  });
});

describe("default ports", () => {
  it("seeds a new check, per type", () => {
    const seed = (type: string) =>
      withDefaultPort(type, tcpModule.fromConfig({})).port;
    expect(seed("tcp")).toBe("443");
    expect(seed("udp")).toBe("53");
    expect(seed("ssh")).toBe("22");
    expect(seed("sftp")).toBe("22");
    expect(seed("ftp")).toBe("21");
    expect(withDefaultPort("ssl", sslModule.fromConfig({})).port).toBe("443");
  });
  it("never overwrites a stored port", () => {
    const state = tcpModule.fromConfig({ port: 6379 });
    expect(withDefaultPort("tcp", state).port).toBe("6379");
  });
  it("leaves types without a default alone", () => {
    const state = tcpModule.fromConfig({});
    expect(withDefaultPort("rdp", state).port).toBe("");
  });
  it("follows the new type's default on a type switch, not a chosen port", () => {
    const tcp = withDefaultPort("tcp", tcpModule.fromConfig({}));
    expect(portForTypeSwitch("tcp", "ssh", tcp).port).toBe("22");
    const custom = { ...tcp, port: "6379" };
    expect(portForTypeSwitch("tcp", "ssh", custom).port).toBe("6379");
  });
});
