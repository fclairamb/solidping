import { describe, expect, it } from "vitest";
import { isAgeRecipient } from "./sealing";

describe("isAgeRecipient", () => {
  it("accepts an age X25519 public key", () => {
    // Printed by `solidping agent region-key generate`.
    expect(isAgeRecipient("age1wrfr7tepkuwr9sslg460az7dypz8g79t26wt0atema5xuxk72p9sc9mnvx")).toBe(true);
    expect(isAgeRecipient("  age1wrfr7tepkuwr9sslg460az7dypz8g79t26wt0atema5xuxk72p9sc9mnvx\n")).toBe(true);
  });

  it("refuses an identity, a truncated key or another prefix", () => {
    expect(isAgeRecipient("AGE-SECRET-KEY-1D77YCDS3EE8WGU5Y4X4DY8RX4AM70PLJCVEQNHW4TEAZQZ5QCTKS0YC4PC")).toBe(false);
    expect(isAgeRecipient("age1wrfr7tepkuwr9sslg460az7dypz8g79t26wt0atema5xuxk72p9sc9mnv")).toBe(false);
    expect(isAgeRecipient("age1wrfr7tepkuwr9sslg460az7dypz8g79t26wt0atema5xuxk72p9sc9mnvb")).toBe(false);
    expect(isAgeRecipient("ssh-ed25519 AAAA")).toBe(false);
    expect(isAgeRecipient("")).toBe(false);
  });
});
