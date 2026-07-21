import { describe, expect, it } from "vitest";

import {
  dedupeStatusPatterns,
  isValidStatusPattern,
  normalizeStatusPattern,
} from "@/lib/http-status";

describe("isValidStatusPattern", () => {
  it.each(["200", "404", "100", "599", "1XX", "5XX"])(
    "accepts the exact/wildcard pattern %s",
    (value) => {
      expect(isValidStatusPattern(value)).toBe(true);
    },
  );

  it("accepts a lowercase wildcard", () => {
    expect(isValidStatusPattern("4xx")).toBe(true);
  });

  it.each(["6XX", "0XX", "99", "20X", "XXX", "600", "", "abc", "20"])(
    "rejects %s",
    (value) => {
      expect(isValidStatusPattern(value)).toBe(false);
    },
  );

  it("trims surrounding whitespace before validating", () => {
    expect(isValidStatusPattern("  200  ")).toBe(true);
    expect(isValidStatusPattern(" 6XX ")).toBe(false);
  });
});

describe("normalizeStatusPattern", () => {
  it("uppercases a wildcard", () => {
    expect(normalizeStatusPattern("4xx")).toBe("4XX");
  });

  it("trims whitespace", () => {
    expect(normalizeStatusPattern("  200  ")).toBe("200");
  });

  it("leaves an already-normalized exact code untouched", () => {
    expect(normalizeStatusPattern("200")).toBe("200");
  });
});

describe("dedupeStatusPatterns", () => {
  it("de-duplicates on the normalized token", () => {
    expect(dedupeStatusPatterns(["200", "4xx", "4XX"])).toEqual([
      "200",
      "4XX",
    ]);
  });

  it("preserves first-seen order", () => {
    expect(dedupeStatusPatterns(["4XX", "200", "4xx"])).toEqual([
      "4XX",
      "200",
    ]);
  });

  it("drops empty/blank entries", () => {
    expect(dedupeStatusPatterns(["200", "", "   "])).toEqual(["200"]);
  });

  it("returns an empty array for an empty or all-blank input", () => {
    expect(dedupeStatusPatterns([])).toEqual([]);
    expect(dedupeStatusPatterns(["", "  "])).toEqual([]);
  });

  it("keeps invalid patterns (dedupe does not validate)", () => {
    expect(dedupeStatusPatterns(["6xx", "99"])).toEqual(["6XX", "99"]);
  });
});
