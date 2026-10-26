import { describe, expect, it } from "vitest";

import type { Check } from "@/api/hooks";
import { orderChecksForGlance } from "@/components/dashboard/glance-order";

function check(slug: string, status: string, enabled = true): Check {
  return { uid: slug, slug, name: slug, status, enabled } as Check;
}

describe("orderChecksForGlance", () => {
  it("does not put a disabled down check in the attention group", () => {
    // solidping-business#17: a disabled check keeps its last status. A paused
    // "down" check used to jump to the top of the glance card as if failing.
    const ordered = orderChecksForGlance([
      check("b-healthy", "up"),
      check("a-paused", "down", false),
      check("c-failing", "down"),
    ]);

    expect(ordered.map((c) => c.slug)).toEqual(["c-failing", "b-healthy", "a-paused"]);
  });
});
