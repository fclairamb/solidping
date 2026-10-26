import { describe, expect, it } from "vitest";

import { customCssHasImport, STARTER_TEMPLATE } from "@/lib/custom-css";

describe("customCssHasImport", () => {
  it.each([
    ["plain rules", ":root { --brand: #f50; }", false],
    ["an @import rule", '@import url("x.css");', true],
    ["upper case", "@IMPORT url(x.css);", true],
    ["inside a comment", "/* @import is not allowed */", true],
  ])("%s", (_name, css, want) => {
    expect(customCssHasImport(css)).toBe(want);
  });

  // Regression: the template mentioned @import in a comment, so inserting it
  // made the stylesheet unsaveable.
  it("accepts the starter template as is", () => {
    expect(customCssHasImport(STARTER_TEMPLATE)).toBe(false);
  });
});
