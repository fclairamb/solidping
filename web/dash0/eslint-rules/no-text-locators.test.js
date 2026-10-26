import { describe, it } from "vitest";
import { RuleTester } from "eslint";
import rule from "./no-text-locators.js";

RuleTester.describe = describe;
RuleTester.it = it;
RuleTester.itOnly = it.only;

const tester = new RuleTester({
  languageOptions: { ecmaVersion: 2022, sourceType: "module" },
});

const e2e = "e2e/some.spec.ts";

tester.run("no-text-locators", rule, {
  valid: [
    { filename: e2e, code: 'await expect(page.getByTestId("x")).toHaveText("Save");' },
    { filename: e2e, code: 'page.getByRole("button", { name: "Save" });' },
    { filename: e2e, code: 'page.locator("[data-testid=save-button]");' },
    { filename: "showcase/specs/a.showcase.ts", code: 'page.getByTestId("x");' },
    // Out of scope: not under e2e/ or showcase/.
    { filename: "src/foo.ts", code: 'page.getByText("Save");' },
  ],
  invalid: [
    { filename: e2e, code: 'page.getByText("Save");', errors: [{ messageId: "method" }] },
    { filename: e2e, code: 'page.getByPlaceholder("Name");', errors: [{ messageId: "method" }] },
    { filename: e2e, code: 'page.getByLabel("Name");', errors: [{ messageId: "method" }] },
    { filename: e2e, code: 'page.locator("button:has-text(\'Save\')");', errors: [{ messageId: "selector" }] },
    { filename: e2e, code: 'page.locator("text=Save");', errors: [{ messageId: "selector" }] },
    { filename: e2e, code: 'page.getByTestId("row").filter({ hasText: "x" });', errors: [{ messageId: "hasText" }] },
    { filename: "showcase/specs/a.showcase.ts", code: 'page.getByText("Save");', errors: [{ messageId: "method" }] },
  ],
});
