/**
 * ESLint rule: no-untranslated-jsx-text
 *
 * Text written straight into JSX is never translated: it renders in English
 * whatever the user's language. Neither locale test can see it (they only read
 * the locale files), so spec 2026-09-26-01 swept `src/` by hand and this rule
 * keeps the result from regrowing.
 *
 * Flagged, when the text holds at least two consecutive letters:
 *   - JSX text children:              <Button>Clear filters</Button>
 *   - string literal children:        <span>{"Needs Action"}</span>
 *   - accessible-name attributes:     aria-label, title, alt, label
 *
 * Not flagged:
 *   - text with no run of letters ("—", "&gt;", "#", "{count}"),
 *   - `placeholder`, which in this app is mostly a sample value
 *     ("example.com", "SELECT 1"). Placeholders that are real prose go through
 *     t() too; web/dash0/CLAUDE.md documents how to sweep them,
 *   - the exact strings in the `allow` option: brand and protocol names and
 *     commands a user types verbatim ("SolidPing", "UDP", "docker run"). Keep
 *     that list short; anything a user reads as a sentence belongs in a locale
 *     file.
 */
const ATTRIBUTES = new Set(["aria-label", "title", "alt", "label"]);

/** @type {import("eslint").Rule.RuleModule} */
export default {
  meta: {
    type: "problem",
    docs: {
      description: "Disallow user-visible text in JSX that bypasses t()",
    },
    schema: [
      {
        type: "object",
        properties: {
          allow: { type: "array", items: { type: "string" } },
        },
        additionalProperties: false,
      },
    ],
    messages: {
      literal:
        'User-visible text "{{text}}" is not translated. Move it to a locale file (en, fr, de, es) and render it with t(). ' +
        "A brand, protocol or verbatim command goes in this rule's allow list in eslint.config.js instead.",
    },
  },

  create(context) {
    const allow = new Set(context.options[0]?.allow ?? []);

    function check(node, raw) {
      const text = raw.replace(/\s+/g, " ").trim();
      if (!/\p{L}{2,}/u.test(text)) return;
      if (allow.has(text)) return;
      context.report({ node, messageId: "literal", data: { text: text.slice(0, 60) } });
    }

    return {
      JSXText(node) {
        check(node, node.value);
      },
      "JSXElement > JSXExpressionContainer > Literal, JSXFragment > JSXExpressionContainer > Literal"(node) {
        if (typeof node.value === "string") check(node, node.value);
      },
      JSXAttribute(node) {
        if (node.name.type !== "JSXIdentifier" || !ATTRIBUTES.has(node.name.name) || !node.value) return;
        const value =
          node.value.type === "Literal"
            ? node.value.value
            : node.value.type === "JSXExpressionContainer" && node.value.expression.type === "Literal"
              ? node.value.expression.value
              : undefined;
        if (typeof value === "string") check(node.value, value);
      },
    };
  },
};
