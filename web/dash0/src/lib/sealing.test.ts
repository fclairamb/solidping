import { Decrypter } from "age-encryption";
import { describe, expect, it } from "vitest";

import fixture from "./__fixtures__/browser-sealed.json";
import {
  generateRegionKey,
  isBrowserSealingEligible,
  recipientFingerprint,
  sealSecrets,
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
});
