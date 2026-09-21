import { describe, expect, it } from "vitest";

import { icmpModule, intervalIsDense, tcpModule } from "./network";
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

// saveIcmpUntouched is saveUntouched for the ICMP module. The four burst keys
// (count / interval / packet_size / ttl) are OWNED since spec 2026-09-21-02,
// so they are no longer carried by the unmodeled-key passthrough — every one
// of these tests pins a direction of that contract.
function saveIcmpUntouched(stored: CheckConfig): CheckConfig {
  const state = icmpModule.fromConfig(stored);
  const { config } = icmpModule.toConfig(state);
  return assembleSubmittedConfig({
    initialConfig: stored,
    ownedKeys: icmpModule.ownedKeys,
    moduleConfig: config,
  });
}

describe("icmpModule — burst field round-trip", () => {
  it("declares every key toConfig can write", () => {
    const state = icmpModule.fromConfig({
      host: "h",
      count: 10,
      interval: "100ms",
      packet_size: 1200,
      ttl: 64,
    });
    const { config } = icmpModule.toConfig(state);
    for (const key of Object.keys(config)) {
      expect(icmpModule.ownedKeys).toContain(key);
    }
  });

  it("round-trips the four burst keys from an API-created check", () => {
    const stored: CheckConfig = {
      host: "gw.acme.com",
      count: 10,
      interval: "100ms",
      packet_size: 1200,
      ttl: 64,
    };
    const state = icmpModule.fromConfig(stored);
    expect(state).toMatchObject({
      host: "gw.acme.com",
      count: "10",
      interval: "100",
      packetSize: "1200",
      ttl: "64",
    });
    expect(icmpModule.toConfig(state).config).toEqual(stored);
    expect(saveIcmpUntouched(stored)).toEqual(stored);
  });

  it("an API-created count survives an unrelated edit (rename-only save)", () => {
    // The regression this spec exists for: somebody opens a check whose
    // burst was configured over the API, changes nothing (or only the name —
    // which is not config), hits save. The owned count/interval keys no
    // longer ride the passthrough, so the module must re-emit them itself or
    // the server's replace-merge deletes them and the burst silently dies.
    const stored: CheckConfig = {
      host: "gw.acme.com",
      count: 10,
      interval: "100ms",
      ttl: 64,
    };
    expect(saveIcmpUntouched(stored)).toEqual(stored);
  });

  it("a fresh check emits only host — unchanged single-ping behaviour", () => {
    const { config, errors } = icmpModule.toConfig({
      host: "gw.acme.com",
      count: "",
      interval: "",
      packetSize: "",
      ttl: "",
    });
    expect(config).toEqual({ host: "gw.acme.com" });
    expect(errors).toEqual([]);
  });

  it("an empty burst section saves untouched without inventing defaults", () => {
    // Only host is stored: the four owned keys are absent, so they must NOT
    // be re-added (that would flip every legacy check to a burst).
    const stored: CheckConfig = { host: "gw.acme.com" };
    expect(saveIcmpUntouched(stored)).toEqual(stored);
  });

  it("clearing a burst field deletes the stored key (omit-to-clear)", () => {
    const stored: CheckConfig = {
      host: "gw.acme.com",
      count: 10,
      interval: "100ms",
    };
    const cleared = { ...icmpModule.fromConfig(stored), count: "" };
    const submitted = assembleSubmittedConfig({
      initialConfig: stored,
      ownedKeys: icmpModule.ownedKeys,
      moduleConfig: icmpModule.toConfig(cleared).config,
    });
    expect(submitted).not.toHaveProperty("count");
    // Interval rides along with count: back at a single ping it is dead
    // config the checker never reads, so dropping the count drops it too.
    expect(submitted).not.toHaveProperty("interval");
  });

  it("interval is only emitted for a burst (count > 1)", () => {
    // At the default single ping the interval is meaningless, so a burst
    // field that lost its count must not leave a dead interval behind — and
    // the form must not silently invent one either.
    const solo = icmpModule.toConfig({
      host: "h",
      count: "",
      interval: "100ms",
      packetSize: "",
      ttl: "",
    });
    expect(solo.config).toEqual({ host: "h" });

    const one = icmpModule.toConfig({
      host: "h",
      count: "1",
      interval: "100ms",
      packetSize: "",
      ttl: "",
    });
    expect(one.config).toEqual({ host: "h", count: 1 });

    const burst = icmpModule.toConfig({
      host: "h",
      count: "5",
      interval: "100ms",
      packetSize: "",
      ttl: "",
    });
    expect(burst.config).toEqual({
      host: "h",
      count: 5,
      interval: "100ms",
    });
  });

  it("explicitly configured values are re-emitted verbatim", () => {
    const state = icmpModule.fromConfig({
      host: "h",
      count: 600,
      interval: "50ms",
      packet_size: 0,
      ttl: 255,
    });
    expect(icmpModule.toConfig(state).config).toEqual({
      host: "h",
      count: 600,
      interval: "50ms",
      // packet_size 0 is a legal value (server validator allows 0–65507) and
      // must round-trip, not be dropped as "unset".
      packet_size: 0,
      ttl: 255,
    });
  });

  it("rejects non-integer counts and malformed intervals", () => {
    const bad = icmpModule.toConfig({
      host: "h",
      count: "10.5",
      interval: "1sec",
      packetSize: "",
      ttl: "",
    });
    expect(bad.errors).toEqual([
      { name: "count", message: "Count must be a whole number of packets" },
    ]);

    const badInterval = icmpModule.toConfig({
      host: "h",
      count: "10",
      interval: "1sec",
      packetSize: "",
      ttl: "",
    });
    expect(badInterval.errors).toEqual([
      {
        name: "interval",
        message: "Interval must be a duration like 100 or 100ms",
      },
    ]);
  });

  it("a bare interval number is milliseconds (ms-default input)", () => {
    // The form field is denominated in ms: "100" means 100ms, and the stored
    // config keeps the Go duration string the checker expects.
    const bare = icmpModule.toConfig({
      host: "h",
      count: "5",
      interval: "100",
      packetSize: "",
      ttl: "",
    });
    expect(bare.config).toEqual({ host: "h", count: 5, interval: "100ms" });

    const decimal = icmpModule.toConfig({
      host: "h",
      count: "5",
      interval: "0.5",
      packetSize: "",
      ttl: "",
    });
    expect(decimal.config).toEqual({ host: "h", count: 5, interval: "0.5ms" });

    // An explicit unit still passes through for power users / API habits.
    const suffixed = icmpModule.toConfig({
      host: "h",
      count: "5",
      interval: "1s",
      packetSize: "",
      ttl: "",
    });
    expect(suffixed.config).toEqual({ host: "h", count: 5, interval: "1s" });
  });

  it("fromConfig normalizes the stored duration to the ms display unit", () => {
    const state = icmpModule.fromConfig({
      host: "h",
      count: 5,
      interval: "1s",
    });
    expect(state.interval).toBe("1000");
    expect(icmpModule.toConfig(state).config).toEqual({
      host: "h",
      count: 5,
      interval: "1000ms",
    });

    // A value the parser cannot read is shown verbatim — the server's
    // validation error, not a silent rewrite, tells the story.
    const garbage = icmpModule.fromConfig({ host: "h", interval: "1sec" });
    expect(garbage.interval).toBe("1sec");
  });

  describe("intervalIsDense — sub-50ms warning", () => {
    it("warns only when the burst is enabled and the interval parses below 50ms", () => {
      // A dense burst: the warning's whole point. The field is ms-default,
      // so a bare number IS milliseconds.
      expect(intervalIsDense("50", "49")).toBe(true);
      expect(intervalIsDense("50", "10")).toBe(true);
      expect(intervalIsDense("50", "49ms")).toBe(true);
      expect(intervalIsDense("50", "10ms")).toBe(true);
      expect(intervalIsDense("50", "0.04s")).toBe(true);

      // At or above the threshold — including the old 50ms floor — stays
      // silent.
      expect(intervalIsDense("50", "50")).toBe(false);
      expect(intervalIsDense("50", "100")).toBe(false);
      expect(intervalIsDense("50", "50ms")).toBe(false);
      expect(intervalIsDense("50", "100ms")).toBe(false);
      expect(intervalIsDense("50", "0.1s")).toBe(false);
      expect(intervalIsDense("50", "1s")).toBe(false);

      // A single ping has no burst spacing: the interval is dead config.
      expect(intervalIsDense("1", "10")).toBe(false);
      expect(intervalIsDense("", "10ms")).toBe(false);

      // Blank/malformed stays silent — the server's validation error, not a
      // warning, is the right surface for those.
      expect(intervalIsDense("50", "")).toBe(false);
      expect(intervalIsDense("50", "1sec")).toBe(false);
      expect(intervalIsDense("50", "s100")).toBe(false);
    });
  });
});
