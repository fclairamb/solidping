import { describe, expect, it } from "vitest";

import {
  isGroupBucketSettled,
  isGroupSectionVisible,
  streamFrontier,
} from "@/lib/check-group-buckets";

const groups = [
  { uid: "g-a", sortOrder: 0 },
  { uid: "g-b", sortOrder: 1 },
  { uid: "g-c", sortOrder: 2 },
];

describe("streamFrontier", () => {
  it("is -Infinity before any check loads", () => {
    expect(streamFrontier([], groups)).toBe(Number.NEGATIVE_INFINITY);
  });

  it("is the highest loaded group sortOrder", () => {
    expect(
      streamFrontier([{ checkGroupUid: "g-a" }, { checkGroupUid: "g-b" }], groups),
    ).toBe(1);
  });

  it("is +Infinity once an ungrouped or unknown-group check loads", () => {
    expect(streamFrontier([{ checkGroupUid: "g-a" }, { checkGroupUid: null }], groups)).toBe(
      Number.POSITIVE_INFINITY,
    );
    expect(streamFrontier([{ checkGroupUid: "g-gone" }], groups)).toBe(
      Number.POSITIVE_INFINITY,
    );
  });
});

describe("isGroupBucketSettled", () => {
  it("settles every group once the stream is drained", () => {
    expect(isGroupBucketSettled(groups[2], Number.NEGATIVE_INFINITY, false)).toBe(true);
  });

  it("settles a group the stream moved past, even with pages left", () => {
    // Issue fclairamb/solidping-business#92: an empty group before the
    // frontier used to show skeletons for as long as hasNextPage was true.
    expect(isGroupBucketSettled(groups[0], 1, true)).toBe(true);
  });

  it("keeps the frontier group and later ones loading while streaming", () => {
    expect(isGroupBucketSettled(groups[1], 1, true)).toBe(false);
    expect(isGroupBucketSettled(groups[2], 1, true)).toBe(false);
  });
});

describe("isGroupSectionVisible", () => {
  it("shows every group, empty ones included, when not filtering", () => {
    expect(isGroupSectionVisible(0, false)).toBe(true);
    expect(isGroupSectionVisible(3, false)).toBe(true);
  });

  it("hides a group without matching checks while filtering, even mid-stream", () => {
    // Issue fclairamb/solidping-business#92: ?labels=env:prod kept showing
    // groups with no matching check.
    expect(isGroupSectionVisible(0, true)).toBe(false);
    expect(isGroupSectionVisible(1, true)).toBe(true);
  });
});
