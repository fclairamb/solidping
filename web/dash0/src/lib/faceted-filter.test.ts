import { describe, expect, it } from "vitest";

import {
  facetedFilterTriggerLabel,
  parseFacetedFilterParam,
  serializeFacetedFilterParam,
} from "@/lib/faceted-filter";

describe("parseFacetedFilterParam", () => {
  it("returns an empty list for undefined/null/empty input", () => {
    expect(parseFacetedFilterParam(undefined)).toEqual([]);
    expect(parseFacetedFilterParam(null)).toEqual([]);
    expect(parseFacetedFilterParam("")).toEqual([]);
  });

  it("splits, trims, and lowercases tokens", () => {
    expect(parseFacetedFilterParam(" Down , Validating ")).toEqual(["down", "validating"]);
  });

  it("drops blank entries from stray commas", () => {
    expect(parseFacetedFilterParam("down,,validating,")).toEqual(["down", "validating"]);
  });

  it("de-duplicates while preserving first-seen order", () => {
    expect(parseFacetedFilterParam("down,up,down")).toEqual(["down", "up"]);
  });

  it("drops tokens outside an allowed set, keeping the rest", () => {
    // A hand-typed or stale URL with an unknown token must not wedge the
    // filter UI — the backend still validates the raw ?status= separately.
    expect(parseFacetedFilterParam("down,bogus,validating", new Set(["down", "validating"]))).toEqual([
      "down",
      "validating",
    ]);
  });

  it("keeps every token when no allowed set is given (e.g. check type)", () => {
    expect(parseFacetedFilterParam("http,some-custom-type")).toEqual(["http", "some-custom-type"]);
  });
});

describe("serializeFacetedFilterParam", () => {
  it("joins values with commas", () => {
    expect(serializeFacetedFilterParam(["down", "validating"])).toBe("down,validating");
  });

  it("returns an empty string for an empty list", () => {
    expect(serializeFacetedFilterParam([])).toBe("");
  });
});

describe("facetedFilterTriggerLabel", () => {
  const options = [
    { value: "up", label: "Up" },
    { value: "down", label: "Down" },
    { value: "validating", label: "Validating" },
    { value: "warning", label: "Warning" },
  ];
  const strings = {
    all: "All statuses",
    count: (count: number) => `${count} statuses`,
    plusOne: (label: string, extra: number) => `${label} +${extra}`,
  };

  it("shows the all-label when nothing is selected", () => {
    expect(facetedFilterTriggerLabel([], options, strings)).toBe("All statuses");
  });

  it("shows the option's own label for a single selection", () => {
    expect(facetedFilterTriggerLabel(["down"], options, strings)).toBe("Down");
  });

  it("falls back to the raw value for a single selection with no matching option", () => {
    expect(facetedFilterTriggerLabel(["degraded"], options, strings)).toBe("degraded");
  });

  it("shows 'label +1' for exactly two selections", () => {
    expect(facetedFilterTriggerLabel(["down", "validating"], options, strings)).toBe("Down +1");
  });

  it("shows 'N statuses' for three or more selections", () => {
    expect(facetedFilterTriggerLabel(["down", "validating", "warning"], options, strings)).toBe(
      "3 statuses",
    );
  });
});
