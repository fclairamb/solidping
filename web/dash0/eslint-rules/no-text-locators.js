/**
 * ESLint rule: no-text-locators
 *
 * Playwright specs in `e2e/` and `showcase/` locate elements with
 * `getByTestId`. A copy change, a rename or a new locale breaks a text locator
 * without any behaviour change, and a video re-cut breaks the same way.
 *
 * Flagged (only in files under e2e/ or showcase/):
 *   - page.getByText(...), getByPlaceholder(...), getByLabel(...)
 *   - locator("button:has-text('x')"), locator("text=Save")
 *   - filter({ hasText: ... }) and locator(sel, { hasText: ... })
 *
 * Not flagged: getByRole (a role + name locator also checks the accessible
 * name), getByTestId, and text used as an assertion on a testid-located
 * element: expect(page.getByTestId("x")).toHaveText("Save").
 */
const METHODS = new Set(["getByText", "getByPlaceholder", "getByLabel"]);
const SELECTOR_TEXT = /:has-text\(|(^|[\s>+~,])text[=/]|:text(-is)?\(|:text-matches\(/;

function inScope(filename) {
  return /(^|[\\/])(e2e|showcase)[\\/]/.test(filename);
}

function staticString(node) {
  if (node.type === "Literal" && typeof node.value === "string") return node.value;
  if (node.type === "TemplateLiteral") return node.quasis.map((q) => q.value.raw).join("");
  return null;
}

/** @type {import("eslint").Rule.RuleModule} */
export default {
  meta: {
    type: "problem",
    docs: { description: "Disallow text-based Playwright locators; use getByTestId" },
    schema: [],
    messages: {
      method: "Do not locate with {{name}}(): add a data-testid and use getByTestId. Assert text with toHaveText on the testid-located element.",
      selector: "Do not locate with text selectors (:has-text, text=): add a data-testid and use getByTestId.",
      hasText: "Do not filter with hasText: locate with getByTestId and assert the text.",
    },
  },
  create(context) {
    if (!inScope(context.filename)) return {};
    return {
      CallExpression(node) {
        const callee = node.callee;
        if (callee.type !== "MemberExpression" || callee.computed || callee.property.type !== "Identifier") return;
        const name = callee.property.name;
        if (METHODS.has(name)) {
          context.report({ node, messageId: "method", data: { name } });
          return;
        }
        if (name === "locator") {
          const first = node.arguments[0];
          const s = first && staticString(first);
          if (s !== null && s !== undefined && SELECTOR_TEXT.test(s)) {
            context.report({ node, messageId: "selector" });
            return;
          }
        }
      },
      Property(node) {
        if (!node.computed && node.key.type === "Identifier" && node.key.name === "hasText") {
          context.report({ node, messageId: "hasText" });
        }
      },
    };
  },
};
