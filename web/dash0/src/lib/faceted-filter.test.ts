import { describe, expect, it } from "vitest";

import {
  facetedFilterBadges,
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

describe("facetedFilterBadges", () => {
  const options = [
    { value: "up", label: "Up" },
    { value: "down", label: "Down" },
    { value: "validating", label: "Validating" },
    { value: "warning", label: "Warning" },
  ];
  const selectedLabel = (count: number) => `${count} selected`;

  it("gives no badge when nothing is selected (the trigger shows the name only)", () => {
    expect(facetedFilterBadges([], options, selectedLabel)).toEqual([]);
  });

  it("gives the option's own label for a single selection", () => {
    expect(facetedFilterBadges(["down"], options, selectedLabel)).toEqual(["Down"]);
  });

  it("gives both labels for two selections", () => {
    expect(facetedFilterBadges(["down", "validating"], options, selectedLabel)).toEqual([
      "Down",
      "Validating",
    ]);
  });

  it("collapses three or more selections into one count badge", () => {
    expect(
      facetedFilterBadges(["down", "validating", "warning"], options, selectedLabel),
    ).toEqual(["3 selected"]);
  });

  it("ignores values that match no option", () => {
    expect(facetedFilterBadges(["bogus"], options, selectedLabel)).toEqual([]);
    expect(facetedFilterBadges(["down", "bogus"], options, selectedLabel)).toEqual(["Down"]);
    // Unknown values must not push a real selection over the 3 threshold.
    expect(facetedFilterBadges(["down", "up", "bogus"], options, selectedLabel)).toEqual([
      "Down",
      "Up",
    ]);
  });
});
