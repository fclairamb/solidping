import { describe, expect, it } from "vitest";

import { suggestQuickTarget } from "./quick-target-suggestion";

describe("suggestQuickTarget", () => {
  // Positive control: a plausible work domain is suggested verbatim.
  it("suggests the bare domain for a work email", () => {
    expect(suggestQuickTarget("alice@acme.com")).toBe("acme.com");
  });

  it("uses the exact email domain, no public-suffix stripping", () => {
    expect(suggestQuickTarget("alice@mail.acme.com")).toBe("mail.acme.com");
  });

  it("lowercases a mixed-case domain", () => {
    expect(suggestQuickTarget("Alice@ACME.com")).toBe("acme.com");
    expect(suggestQuickTarget("alice@AcMe.CoM")).toBe("acme.com");
  });

  it("supports a plus-tagged local part", () => {
    expect(suggestQuickTarget("alice+tag@acme.com")).toBe("acme.com");
  });

  it("resolves to the domain after the LAST @, even with an @ in the local part", () => {
    expect(suggestQuickTarget('"a@b"@acme.com')).toBe("acme.com");
  });

  it("passes a bare IP literal through unchanged", () => {
    expect(suggestQuickTarget("alice@192.0.2.10")).toBe("192.0.2.10");
  });

  // No @, empty, or whitespace-only input.
  it.each([null, undefined, "", "   ", "not-an-email", "alice"])(
    "returns null for malformed input %p",
    (input) => {
      expect(suggestQuickTarget(input)).toBeNull();
    },
  );

  it("returns null when there is nothing after the last @", () => {
    expect(suggestQuickTarget("alice@")).toBeNull();
    expect(suggestQuickTarget("alice@   ")).toBeNull();
    expect(suggestQuickTarget("@")).toBeNull();
  });

  // Every free-webmail domain from server/internal/handlers/auth/
  // autojoin_regex.go's freeMailDomains list must yield no suggestion.
  const FREE_MAIL_DOMAINS = [
    "gmail.com",
    "googlemail.com",
    "yahoo.com",
    "outlook.com",
    "hotmail.com",
    "live.com",
    "msn.com",
    "icloud.com",
    "me.com",
    "protonmail.com",
    "proton.me",
    "aol.com",
    "gmx.com",
    "mail.com",
    "yandex.com",
    "tutanota.com",
    "zoho.com",
    "qq.com",
    "163.com",
    "126.com",
  ];

  it.each(FREE_MAIL_DOMAINS)("returns null for free-webmail domain %s", (domain) => {
    expect(suggestQuickTarget(`alice@${domain}`)).toBeNull();
  });

  it("is case-insensitive for the denylist too", () => {
    expect(suggestQuickTarget("alice@Gmail.COM")).toBeNull();
  });

  // Not a plausible host: "/", ":", whitespace, or a leading/trailing dot.
  it("returns null for a domain containing a path", () => {
    expect(suggestQuickTarget("alice@acme.com/health")).toBeNull();
  });

  it("returns null for a domain carrying a port", () => {
    expect(suggestQuickTarget("alice@acme.com:8443")).toBeNull();
  });

  it("returns null for a domain containing whitespace", () => {
    expect(suggestQuickTarget("alice@acme .com")).toBeNull();
  });

  it("returns null for a leading- or trailing-dot domain", () => {
    expect(suggestQuickTarget("alice@.acme.com")).toBeNull();
    expect(suggestQuickTarget("alice@acme.com.")).toBeNull();
  });
});
