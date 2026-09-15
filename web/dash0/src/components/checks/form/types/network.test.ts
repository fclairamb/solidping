import { describe, expect, it } from "vitest";

import { tcpModule } from "./network";
import { assembleSubmittedConfig, type CheckConfig } from "./common";

// saveUntouched reproduces exactly what the shared form submits when a check is
// opened and saved without touching a field.
function saveUntouched(stored: CheckConfig): CheckConfig {
  const state = tcpModule.fromConfig(stored);
  const { config } = tcpModule.toConfig(state);
  return assembleSubmittedConfig({
    initialConfig: stored,
    ownedKeys: tcpModule.ownedKeys,
    moduleConfig: config,
  });
}

describe("tcpModule — payload & reply round-trip", () => {
  it("round-trips all six keys in contains mode", () => {
    const stored: CheckConfig = {
      host: "redis.acme.com",
      port: 6379,
      send_data: String.raw`PING\r\n`,
      send_encoding: "escaped",
      expect_data: "2b504f4e47",
      expect_encoding: "hex",
    };

    const state = tcpModule.fromConfig(stored);
    expect(state).toMatchObject({
      host: "redis.acme.com",
      port: "6379",
      sendData: String.raw`PING\r\n`,
      sendEncoding: "escaped",
      expectMode: "contains",
      expectValue: "2b504f4e47",
      expectEncoding: "hex",
    });

    expect(tcpModule.toConfig(state).config).toEqual(stored);
    expect(saveUntouched(stored)).toEqual(stored);
  });

  it("round-trips expect_pattern as regex mode", () => {
    const stored: CheckConfig = {
      host: "mail.acme.com",
      port: 25,
      expect_pattern: String.raw`^220 .* ESMTP`,
    };

    const state = tcpModule.fromConfig(stored);
    expect(state.expectMode).toBe("regex");
    expect(state.expectValue).toBe(String.raw`^220 .* ESMTP`);
    expect(saveUntouched(stored)).toEqual(stored);
  });

  it("defaults a NEW check to escaped but never re-encodes a stored payload", () => {
    // Nothing stored: escaped, so `\r\n` can be typed into the textarea.
    expect(tcpModule.fromConfig({}).sendEncoding).toBe("escaped");

    // A payload stored WITHOUT an encoding means `text` on the backend, and
    // must keep meaning that — re-tagging it `escaped` would change the bytes
    // a stored check puts on the wire.
    expect(
      tcpModule.fromConfig({ send_data: String.raw`a\b` }).sendEncoding,
    ).toBe("text");
    expect(
      saveUntouched({ host: "h", port: 1, send_data: String.raw`a\b` }),
    ).toEqual({
      host: "h",
      port: 1,
      send_data: String.raw`a\b`,
    });
  });

  it("omits send_encoding / expect_encoding when they are the text default", () => {
    const { config } = tcpModule.toConfig({
      host: "h",
      port: "1",
      sendData: "PING",
      sendEncoding: "text",
      expectMode: "contains",
      expectValue: "+PONG",
      expectEncoding: "text",
    });
    expect(config).toEqual({
      host: "h",
      port: 1,
      send_data: "PING",
      expect_data: "+PONG",
    });
  });

  it("a cleared expect input omits BOTH expect keys", () => {
    // Omit-to-clear: expect_data / expect_pattern are in ownedKeys, so an
    // absent key really deletes the stored one instead of being carried
    // through by the unmodeled-key passthrough.
    const stored: CheckConfig = {
      host: "h",
      port: 1,
      expect_data: "+PONG",
      expect_encoding: "hex",
    };
    const cleared = { ...tcpModule.fromConfig(stored), expectValue: "" };
    const submitted = assembleSubmittedConfig({
      initialConfig: stored,
      ownedKeys: tcpModule.ownedKeys,
      moduleConfig: tcpModule.toConfig(cleared).config,
    });
    expect(submitted).not.toHaveProperty("expect_data");
    expect(submitted).not.toHaveProperty("expect_encoding");
    expect(submitted).not.toHaveProperty("expect_pattern");
  });

  it("switching expect mode deletes the other key", () => {
    const stored: CheckConfig = { host: "h", port: 1, expect_data: "+PONG" };
    const switched = {
      ...tcpModule.fromConfig(stored),
      expectMode: "regex" as const,
      expectValue: String.raw`^\+PONG`,
    };
    const submitted = assembleSubmittedConfig({
      initialConfig: stored,
      ownedKeys: tcpModule.ownedKeys,
      moduleConfig: tcpModule.toConfig(switched).config,
    });
    expect(submitted.expect_pattern).toBe(String.raw`^\+PONG`);
    expect(submitted).not.toHaveProperty("expect_data");

    // ...and back again.
    const back = {
      ...tcpModule.fromConfig(submitted),
      expectMode: "contains" as const,
    };
    const reverted = assembleSubmittedConfig({
      initialConfig: submitted,
      ownedKeys: tcpModule.ownedKeys,
      moduleConfig: tcpModule.toConfig(back).config,
    });
    expect(reverted.expect_data).toBe(String.raw`^\+PONG`);
    expect(reverted).not.toHaveProperty("expect_pattern");
  });

  it("still carries unmodeled keys through untouched", () => {
    const stored: CheckConfig = {
      host: "h",
      port: 1,
      tls: true,
      tls_server_name: "acme.com",
    };
    expect(saveUntouched(stored)).toEqual(stored);
  });
});
