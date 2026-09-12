import { describe, expect, it } from "vitest";

import {
  carryOverTarget,
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

  it.each(["icmp", "ssl"] as const)(
    "a bare trailing slash is not a path the value cannot recover from (%s)",
    (type) => {
      // `targetAppliesTo` answers about the literal string, so a trailing
      // slash is still "not a bare host" here — carryOverTarget is what strips
      // it instead of discarding the value. See its own tests below.
      expect(targetAppliesTo(type, "example.com/")).toBe(false);
      expect(carryOverTarget(type, "example.com/")).toBe("example.com");
    },
  );

  it.each(["icmp", "ssl"] as const)("clears a value with spaces for %s", (type) => {
    expect(targetAppliesTo(type, "two words")).toBe(false);
  });

  // ICMP has no ports, and the SSL checker takes its port in a SEPARATE
  // `port` config field — so `example.com:8443` in the host field is never
  // usable. Neither backend validates the host's shape (checkicmp has no rule
  // at all, checkssl only rejects an empty host), so letting it through would
  // create a check that is accepted, stored, and fails on its first run.
  it.each(["icmp", "ssl"] as const)("clears a host:port pair for %s", (type) => {
    expect(targetAppliesTo(type, "example.com:8443")).toBe(false);
    expect(targetAppliesTo(type, "example.com:80")).toBe(false);
    expect(targetAppliesTo(type, "[2001:db8::1]:443")).toBe(false);
  });

  it("keeps a host:port pair for http, where a port is a normal target", () => {
    expect(targetAppliesTo("http", "example.com:8443")).toBe(true);
  });

  // The colon rule must not swallow IPv6: an address has two or more colons,
  // a host:port pair has exactly one. `2001:db8::1` is a legitimate ping
  // target and SSL host.
  it.each(["icmp", "ssl"] as const)("keeps an IPv6 literal for %s", (type) => {
    expect(targetAppliesTo(type, "2001:db8::1")).toBe(true);
    expect(targetAppliesTo(type, "::1")).toBe(true);
    expect(targetAppliesTo(type, "fe80::1%eth0")).toBe(true);
    expect(targetAppliesTo(type, "[2001:db8::1]")).toBe(true);
  });

  it("ignores surrounding whitespace when deciding", () => {
    expect(targetAppliesTo("ssl", "  example.com  ")).toBe(true);
    expect(targetAppliesTo("ssl", "  https://example.com  ")).toBe(false);
  });
});

describe("carryOverTarget", () => {
  it("keeps a hostname on every chip switch", () => {
    expect(carryOverTarget("ssl", "example.com")).toBe("example.com");
    expect(carryOverTarget("icmp", "example.com")).toBe("example.com");
    expect(carryOverTarget("http", "example.com")).toBe("example.com");
  });

  it("keeps the user's own text untouched when nothing needs tidying", () => {
    // Not even whitespace is rewritten under them while they are still typing.
    expect(carryOverTarget("ssl", "  example.com  ")).toBe("  example.com  ");
  });

  it("trims a recoverable trailing slash rather than discarding the value", () => {
    expect(carryOverTarget("icmp", "example.com/")).toBe("example.com");
    expect(carryOverTarget("ssl", "example.com///")).toBe("example.com");
  });

  it("clears a value that cannot be a host", () => {
    expect(carryOverTarget("icmp", "https://example.com")).toBe("");
    expect(carryOverTarget("ssl", "example.com/health")).toBe("");
    expect(carryOverTarget("icmp", "example.com:8443")).toBe("");
  });

  it("never clears anything for http", () => {
    expect(carryOverTarget("http", "https://example.com/health")).toBe(
      "https://example.com/health",
    );
    expect(carryOverTarget("http", "example.com:8443")).toBe("example.com:8443");
  });

  it("leaves an empty field empty", () => {
    expect(carryOverTarget("ssl", "")).toBe("");
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

  it.each(["icmp", "ssl"] as const)(
    "drops a trailing slash before sending for %s",
    (type) => {
      expect(normalizeTarget(type, "example.com/")).toBe("example.com");
    },
  );

  it("keeps an explicit port on an http target and still adds https", () => {
    // Decided: the scheme is NOT guessed from the port (`:80` → http) — that
    // is a second heuristic that gets `:8080` wrong just as easily.
    expect(normalizeTarget("http", "example.com:8443")).toBe(
      "https://example.com:8443",
    );
    expect(normalizeTarget("http", "http://example.com:8080")).toBe(
      "http://example.com:8080",
    );
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

  it.each(["icmp", "ssl"] as const)(
    "rejects a host:port pair for %s, which no backend would catch",
    (type) => {
      expect(validateTarget(type, "example.com:8443")).toBe("invalid");
      expect(validateTarget(type, "[2001:db8::1]:443")).toBe("invalid");
    },
  );

  it.each(["icmp", "ssl"] as const)("accepts an IPv6 literal for %s", (type) => {
    expect(validateTarget(type, "2001:db8::1")).toBeNull();
    expect(validateTarget(type, "::1")).toBeNull();
  });

  it("accepts a host:port pair for http", () => {
    expect(validateTarget("http", "example.com:8443")).toBeNull();
  });

  it.each(["icmp", "ssl"] as const)(
    "accepts a trailing slash for %s (it is stripped, not rejected)",
    (type) => {
      expect(validateTarget(type, "example.com/")).toBeNull();
    },
  );

  it.each(["icmp", "ssl"] as const)("rejects a dotted edge case for %s", (type) => {
    expect(validateTarget(type, ".")).toBe("invalid");
    expect(validateTarget(type, ".example.com")).toBe("invalid");
    expect(validateTarget(type, "example.com.")).toBe("invalid");
  });
});
