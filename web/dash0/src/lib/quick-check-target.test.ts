import { describe, expect, it } from "vitest";

import {
  normalizeTarget,
  targetAppliesTo,
  validateTarget,
  type QuickCheckType,
} from "./quick-check-target";

// Spec 2026-09-12-04, defect 2. The pre-fix hero ran `setValue("")` on EVERY
// chip click, so the property these tests pin — "a value that still applies to
// the newly picked type is kept" — was false for every input in every
// direction. The `keeps` rows below are the ones that fail against that
// behavior; the `clears` rows are the counter-examples that stop the fix from
// degenerating into "never clear anything".
describe("targetAppliesTo", () => {
  const ALL: QuickCheckType[] = ["http", "icmp", "ssl"];

  it.each(ALL)("keeps a bare hostname for %s", (type) => {
    expect(targetAppliesTo(type, "example.com")).toBe(true);
  });

  it.each(ALL)("keeps a bare IPv4 address for %s", (type) => {
    expect(targetAppliesTo(type, "192.0.2.10")).toBe(true);
  });

  it.each(ALL)("treats an empty value as applicable for %s", (type) => {
    expect(targetAppliesTo(type, "")).toBe(true);
    expect(targetAppliesTo(type, "   ")).toBe(true);
  });

  it("keeps a full URL for http", () => {
    expect(targetAppliesTo("http", "https://example.com/health?x=1")).toBe(true);
  });

  it.each(["icmp", "ssl"] as const)(
    "clears a scheme-carrying URL moving to %s",
    (type) => {
      expect(targetAppliesTo(type, "https://example.com")).toBe(false);
      expect(targetAppliesTo(type, "http://example.com/health")).toBe(false);
    },
  );

  it.each(["icmp", "ssl"] as const)("clears a path or query for %s", (type) => {
    expect(targetAppliesTo(type, "example.com/health")).toBe(false);
    expect(targetAppliesTo(type, "example.com?x=1")).toBe(false);
    expect(targetAppliesTo(type, "example.com#frag")).toBe(false);
  });

  it.each(["icmp", "ssl"] as const)("clears a value with spaces for %s", (type) => {
    expect(targetAppliesTo(type, "two words")).toBe(false);
  });

  it.each(["icmp", "ssl"] as const)(
    "keeps a host:port pair and an IPv6 literal for %s (a colon is not a scheme)",
    (type) => {
      expect(targetAppliesTo(type, "example.com:8443")).toBe(true);
      expect(targetAppliesTo(type, "2001:db8::1")).toBe(true);
    },
  );

  it("ignores surrounding whitespace when deciding", () => {
    expect(targetAppliesTo("ssl", "  example.com  ")).toBe(true);
    expect(targetAppliesTo("ssl", "  https://example.com  ")).toBe(false);
  });
});

describe("normalizeTarget", () => {
  it("promotes a scheme-less http target to https", () => {
    expect(normalizeTarget("http", "example.com")).toBe("https://example.com");
    expect(normalizeTarget("http", "  example.com/health  ")).toBe(
      "https://example.com/health",
    );
  });

  it("leaves an explicit scheme alone", () => {
    expect(normalizeTarget("http", "http://example.com")).toBe(
      "http://example.com",
    );
    expect(normalizeTarget("http", "https://example.com")).toBe(
      "https://example.com",
    );
  });

  it.each(["icmp", "ssl"] as const)("never adds a scheme for %s", (type) => {
    expect(normalizeTarget(type, "example.com")).toBe("example.com");
    expect(normalizeTarget(type, "  example.com  ")).toBe("example.com");
  });

  it("leaves an empty value empty", () => {
    expect(normalizeTarget("http", "   ")).toBe("");
  });
});

describe("validateTarget", () => {
  it.each(["http", "icmp", "ssl"] as const)(
    "reports an empty %s target as empty, not invalid",
    (type) => {
      expect(validateTarget(type, "")).toBe("empty");
      expect(validateTarget(type, "   ")).toBe("empty");
    },
  );

  it.each(["http", "icmp", "ssl"] as const)("accepts a hostname for %s", (type) => {
    expect(validateTarget(type, "example.com")).toBeNull();
  });

  it("accepts a full URL for http", () => {
    expect(validateTarget("http", "https://example.com/health")).toBeNull();
  });

  it("rejects a non-http scheme for http", () => {
    expect(validateTarget("http", "ftp://example.com")).toBe("invalid");
  });

  it.each(["icmp", "ssl"] as const)("rejects a URL for %s", (type) => {
    expect(validateTarget(type, "https://example.com")).toBe("invalid");
  });

  it.each(["icmp", "ssl"] as const)("rejects a dotted edge case for %s", (type) => {
    expect(validateTarget(type, ".")).toBe("invalid");
    expect(validateTarget(type, ".example.com")).toBe("invalid");
    expect(validateTarget(type, "example.com.")).toBe("invalid");
  });
});
