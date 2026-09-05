import { describe, expect, it } from "vitest";

import {
  firstNameOf,
  randomOrgName,
  randomOrgNameSeed,
  suggestOrgName,
} from "@/lib/org-name-suggestion";
import { ORG_SLUG_MIN_LEN, orgSlugify } from "@/lib/org-slug";

describe("firstNameOf", () => {
  it("takes the first token of a multi-token name", () => {
    expect(firstNameOf("Alice Smith")).toBe("Alice");
    expect(firstNameOf("Alice de la Fontaine")).toBe("Alice");
  });

  it("returns a single token unchanged", () => {
    expect(firstNameOf("Alice")).toBe("Alice");
  });

  it("ignores leading and trailing whitespace", () => {
    expect(firstNameOf("   Alice   ")).toBe("Alice");
    expect(firstNameOf("\tAlice\tSmith\n")).toBe("Alice");
    expect(firstNameOf("Alice Smith".replace(" ", " "))).toBe("Alice");
  });

  it("returns null when there is no name at all", () => {
    expect(firstNameOf(undefined)).toBeNull();
    expect(firstNameOf(null)).toBeNull();
    expect(firstNameOf("")).toBeNull();
    expect(firstNameOf("   ")).toBeNull();
  });

  it("returns null for a first token with no letter or digit", () => {
    // "'s organization" is not a name — these must fall through to the
    // random proposal rather than producing a nonsense possessive.
    expect(firstNameOf("- -")).toBeNull();
    expect(firstNameOf("!!! Smith")).toBeNull();
  });

  it("keeps non-Latin names", () => {
    // The regression this guards: an /[a-z]/i test would have thrown these
    // away and shown a random English name to someone who told us their name.
    expect(firstNameOf("李雷")).toBe("李雷");
    expect(firstNameOf("Алиса Иванова")).toBe("Алиса");
    expect(firstNameOf("Ünal Öztürk")).toBe("Ünal");
    expect(firstNameOf("محمد علي")).toBe("محمد");
  });
});

describe("randomOrgName", () => {
  it("is deterministic for a given seed", () => {
    expect(randomOrgName(0)).toBe(randomOrgName(0));
    expect(randomOrgName(7)).toBe(randomOrgName(7));
    expect(randomOrgName(123)).toBe(randomOrgName(123));
  });

  it("varies across seeds", () => {
    const names = new Set(
      Array.from({ length: 24 }, (_, index) => randomOrgName(index)),
    );
    expect(names.size).toBeGreaterThan(1);
  });

  it("always produces two words", () => {
    for (let seed = 0; seed < 60; seed++) {
      expect(randomOrgName(seed).split(" ")).toHaveLength(2);
    }
  });

  it("always slugifies to a valid org-slug base", () => {
    // The name is submitted as-is and the SERVER derives the slug from it, so a
    // proposal that normalizes to "" (or to something under the 3-char floor)
    // would hand the newcomer the generic "org" fallback address.
    for (let seed = 0; seed < 60; seed++) {
      const slug = orgSlugify(randomOrgName(seed));
      expect(slug.length).toBeGreaterThanOrEqual(ORG_SLUG_MIN_LEN);
      expect(slug).toMatch(/^[a-z0-9][a-z0-9-]*[a-z0-9]$/);
    }
  });

  it("works without a seed", () => {
    expect(randomOrgName()).toMatch(/^[A-Z][a-z]+ [A-Z][a-z]+$/);
  });
});

describe("randomOrgNameSeed", () => {
  it("produces a seed randomOrgName accepts", () => {
    for (let i = 0; i < 20; i++) {
      const seed = randomOrgNameSeed();
      expect(Number.isInteger(seed)).toBe(true);
      expect(seed).toBeGreaterThanOrEqual(0);
      expect(randomOrgName(seed)).toBe(randomOrgName(seed));
    }
  });
});

describe("suggestOrgName", () => {
  it("proposes the personal form when a first name is known", () => {
    expect(suggestOrgName("Alice Smith")).toEqual({
      kind: "personal",
      firstName: "Alice",
    });
  });

  it("falls back to a random name when no name was given", () => {
    expect(suggestOrgName(undefined, 3)).toEqual({
      kind: "random",
      name: randomOrgName(3),
    });
    expect(suggestOrgName("", 3).kind).toBe("random");
    expect(suggestOrgName("   ", 3).kind).toBe("random");
  });

  it("does not fall back to the email local part", () => {
    // Decision recorded in the spec: first name from the profile, else random.
    // An address is not a name — "ops@" or "noreply@" would read absurdly.
    const suggestion = suggestOrgName(null, 5);
    expect(suggestion.kind).toBe("random");
    expect(JSON.stringify(suggestion)).not.toContain("@");
  });
});
