import { describe, expect, it } from "vitest";

import { isValidEmail, parseEmailList } from "@/lib/email";

describe("isValidEmail", () => {
  it("accepts a plain address", () => {
    expect(isValidEmail("ops@example.com")).toBe(true);
  });

  it("accepts addresses with subdomains and plus-tags", () => {
    expect(isValidEmail("a.b+tag@mail.sub.example.com")).toBe(true);
  });

  it("rejects a missing @", () => {
    expect(isValidEmail("opsexample.com")).toBe(false);
  });

  it("rejects a missing domain dot", () => {
    expect(isValidEmail("ops@example")).toBe(false);
  });

  it("rejects embedded whitespace", () => {
    expect(isValidEmail("ops oncall@example.com")).toBe(false);
    expect(isValidEmail("ops@exa mple.com")).toBe(false);
  });

  it("rejects an empty string", () => {
    expect(isValidEmail("")).toBe(false);
  });
});

describe("parseEmailList", () => {
  it("splits on spaces", () => {
    expect(parseEmailList("a@x.com b@y.com")).toEqual(["a@x.com", "b@y.com"]);
  });

  it("splits on commas", () => {
    expect(parseEmailList("a@x.com,b@y.com")).toEqual(["a@x.com", "b@y.com"]);
  });

  it("splits on semicolons", () => {
    expect(parseEmailList("a@x.com;b@y.com")).toEqual(["a@x.com", "b@y.com"]);
  });

  it("splits on newlines", () => {
    expect(parseEmailList("a@x.com\nb@y.com")).toEqual(["a@x.com", "b@y.com"]);
  });

  it("splits on a mix of separators and extra whitespace", () => {
    expect(parseEmailList(" a@x.com,  b@y.com;\nc@z.com  d@w.com ")).toEqual([
      "a@x.com",
      "b@y.com",
      "c@z.com",
      "d@w.com",
    ]);
  });

  it("trims and drops empty entries", () => {
    expect(parseEmailList("  a@x.com ,, ; b@y.com  ")).toEqual([
      "a@x.com",
      "b@y.com",
    ]);
  });

  it("de-duplicates exact-match entries while preserving case", () => {
    expect(parseEmailList("a@x.com a@x.com A@x.com")).toEqual([
      "a@x.com",
      "A@x.com",
    ]);
  });

  it("returns an empty array for blank input", () => {
    expect(parseEmailList("   ")).toEqual([]);
    expect(parseEmailList("")).toEqual([]);
  });
});
