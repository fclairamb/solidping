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
 *   - ternary branches:               <span>{up ? "All Up" : "Needs Action"}</span>
 *   - template literal text:          <span>{`${n} checks`}</span>
 *   - accessible-name attributes:     aria-label, title, alt, label (same
 *     forms: literal, ternary branch, template literal)
 *
 * Not flagged:
 *   - text with no run of letters ("—", "&gt;", "#", "{count}"),
 *   - `placeholder`, which in this app is mostly a sample value
 *     ("example.com", "SELECT 1"). Placeholders that are real prose go through
 *     t() too; web/dash0/CLAUDE.md documents how to sweep them,
 *   - text made only of the phrases in the `allow` option (whole words), plus
 *     punctuation and numbers: brand and protocol names, units and commands a
 *     user types verbatim ("SolidPing", "UDP", "ms", "docker run"), so
 *     `{`${n} ms`}` passes while "Powered by SolidPing" does not. Keep that
 *     list short; anything a user reads as a sentence belongs in a locale file.
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
    // Longest first, so "docker compose" is removed before a shorter phrase
    // could split it. Each phrase only matches as whole words.
    const allowPatterns = [...(context.options[0]?.allow ?? [])]
      .sort((a, b) => b.length - a.length)
      .map(
        (phrase) =>
          new RegExp(`(?<![\\p{L}\\p{N}])${phrase.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}(?![\\p{L}\\p{N}])`, "gu"),
      );

    function check(node, raw) {
      const text = raw.replace(/\s+/g, " ").trim();
      if (!/\p{L}{2,}/u.test(text)) return;
      const rest = allowPatterns.reduce((acc, pattern) => acc.replace(pattern, " "), text);
      if (!/\p{L}{2,}/u.test(rest)) return;
      context.report({ node, messageId: "literal", data: { text: text.slice(0, 60) } });
    }

    // A rendered expression: a string literal, the static text of a template
    // literal, or either branch of a (possibly nested) ternary. Anything else
    // (a t() call, a variable) is not text this rule can judge.
    function checkExpression(node) {
      if (!node) return;
      if (node.type === "Literal") {
        if (typeof node.value === "string") check(node, node.value);
      } else if (node.type === "TemplateLiteral") {
        check(node, node.quasis.map((q) => q.value.cooked ?? "").join(" "));
      } else if (node.type === "ConditionalExpression") {
        checkExpression(node.consequent);
        checkExpression(node.alternate);
      }
    }

    return {
      JSXText(node) {
        check(node, node.value);
      },
      "JSXElement > JSXExpressionContainer, JSXFragment > JSXExpressionContainer"(node) {
        checkExpression(node.expression);
      },
      JSXAttribute(node) {
        if (node.name.type !== "JSXIdentifier" || !ATTRIBUTES.has(node.name.name) || !node.value) return;
        if (node.value.type === "Literal") {
          if (typeof node.value.value === "string") check(node.value, node.value.value);
        } else if (node.value.type === "JSXExpressionContainer") {
          checkExpression(node.value.expression);
        }
      },
    };
  },
};
