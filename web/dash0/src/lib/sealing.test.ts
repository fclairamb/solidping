import { Decrypter } from "age-encryption";
import { describe, expect, it } from "vitest";

import fixture from "./__fixtures__/browser-sealed.json";
import bindingFixture from "./__fixtures__/target-binding.json";
import {
  BINDING_KEY,
  canonicalTarget,
  normalizeTargetURL,
  targetChanged,
  targetDigest,
  generateRegionKey,
  isBrowserSealingEligible,
  recipientFingerprint,
  sealSecrets,
  splitSecrets,
  withoutSecretKeys,
} from "./sealing";

function fromBase64(value: string): Uint8Array {
  return Uint8Array.from(atob(value), (c) => c.charCodeAt(0));
}

async function open(identity: string, ct: string): Promise<unknown> {
  const decrypter = new Decrypter();
  decrypter.addIdentity(identity);
  return JSON.parse(await decrypter.decrypt(fromBase64(ct), "text"));
}

describe("sealing", () => {
  it("computes the same fingerprint as credentials.RecipientFingerprint", async () => {
    // The Go side asserts the same fixture (sealing_browser_fixture_test.go).
    expect(await recipientFingerprint(fixture.recipient)).toBe(fixture.fingerprint);
  });

  it("opens the committed fixture with its identity", async () => {
    expect(await open(fixture.identity, fixture.ct)).toEqual(fixture.secrets);
  });

  it("seals to every recipient, with sorted keys and fingerprints", async () => {
    const other = await generateRegionKey();
    const sealed = await sealSecrets(
      [{ recipient: other.recipient }, { recipient: fixture.recipient }],
      { password: "p", basicAuth: "a:b" },
    );
    expect(sealed.keys).toEqual(["basicAuth", "password"]);
    expect(sealed.fingerprints).toEqual(
      [fixture.fingerprint, other.fingerprint].sort(),
    );
    expect(await open(fixture.identity, sealed.ct)).toEqual({ password: "p", basicAuth: "a:b" });
    expect(await open(other.identity, sealed.ct)).toEqual({ password: "p", basicAuth: "a:b" });
  });

  it("refuses an empty recipient list", async () => {
    await expect(sealSecrets([], { password: "p" })).rejects.toThrow();
  });

  it("generates a region key whose recipient matches its identity", async () => {
    const key = await generateRegionKey();
    expect(key.identity.startsWith("AGE-SECRET-KEY-1")).toBe(true);
    expect(key.recipient.startsWith("age1")).toBe(true);
    expect(key.fingerprint).toBe(await recipientFingerprint(key.recipient));
  });

  it("is eligible only when every region is private", () => {
    expect(isBrowserSealingEligible(["@dc1", "@dc2"])).toBe(true);
    expect(isBrowserSealingEligible(["@dc1", "eu-fr"])).toBe(false);
    expect(isBrowserSealingEligible([])).toBe(false);
  });

  it("splits secrets out of the config and folds http basic auth", () => {
    expect(
      splitSecrets(
        "http",
        { url: "u", username: "alice", password: "pw", secretHeaders: { "X-T": "t" } },
        ["basicAuth", "password", "secretHeaders"],
      ),
    ).toEqual({
      publicConfig: { url: "u" },
      secrets: { basicAuth: "alice:pw", secretHeaders: { "X-T": "t" } },
    });
    expect(splitSecrets("ssh", { host: "h", password: "" }, ["password"])).toEqual({
      publicConfig: { host: "h", password: "" },
      secrets: {},
    });
  });

  it("a locked form never sends a cleared secret key", () => {
    // "add a secret header row, remove it, save" leaves secretHeaders: {}.
    expect(
      withoutSecretKeys(
        "http",
        { url: "https://acme.com", secretHeaders: {}, username: "", password: "", basicAuth: "" },
        ["basicAuth", "secretHeaders"],
      ),
    ).toEqual({ url: "https://acme.com" });
    expect(withoutSecretKeys("ssh", { host: "h", password: null }, ["password"])).toEqual({ host: "h" });
  });

  // Target binding (spec 2026-11-11-01): the Go side reads the same fixture
  // (credentials/binding_test.go).
  it("gives the Go canonical target and digest for every shared vector", async () => {
    expect(bindingFixture.cases.length).toBeGreaterThan(0);
    for (const vector of bindingFixture.cases) {
      const config = vector.config as Record<string, unknown>;
      expect(canonicalTarget(vector.targetFields, config), vector.name).toBe(vector.canonical);
      expect(await targetDigest(vector.targetFields, config), vector.name).toBe(vector.digest);
    }
  });

  it("normalises a target URL like the Go side", () => {
    expect(normalizeTargetURL("HTTPS://Acme.COM")).toBe("https://acme.com:443");
    expect(normalizeTargetURL("http://[::1]/")).toBe("http://[::1]:80/");
    expect(normalizeTargetURL("wss://u:P@Acme.com:9000/x")).toBe("wss://u:P@acme.com:9000/x");
    expect(normalizeTargetURL("not a url")).toBe("not a url");
  });

  it("tells a target change from any other edit", () => {
    const fields = ["tunnelCheckUid", "url"];
    const before = { url: "https://db.acme.com", timeout: "5s" };
    expect(targetChanged(fields, before, { url: "https://DB.acme.com:443", timeout: "9s" })).toBe(false);
    expect(targetChanged(fields, before, { url: "https://evil.example" })).toBe(true);
    expect(targetChanged(fields, before, { ...before, tunnelCheckUid: "bastion" })).toBe(true);
  });

  it("seals the binding next to the secrets, out of the key list", async () => {
    const target = { type: "http", targetFields: ["tunnelCheckUid", "url"], config: { url: "https://db.acme.com" } };
    const sealed = await sealSecrets([{ recipient: fixture.recipient }], { basicAuth: "a:b" }, target);
    expect(sealed.keys).toEqual(["basicAuth"]);
    expect(sealed.bound).toBe(true);
    expect(await open(fixture.identity, sealed.ct)).toEqual({
      basicAuth: "a:b",
      [BINDING_KEY]: { type: "http", digest: await targetDigest(target.targetFields, target.config) },
    });
  });
});
