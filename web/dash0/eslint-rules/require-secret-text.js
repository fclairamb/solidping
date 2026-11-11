/**
 * ESLint rule: require-secret-text
 *
 * dash0 records PostHog sessions with no text masking, on purpose (see
 * src/lib/analytics.ts). A credential rendered as page text therefore reaches
 * the PostHog project, unless it sits under a `ph-no-capture` element.
 * GHSA-c9qq-4m99-c7f6 was exactly that: the browser-generated region key in a
 * plain <code> (spec 2026-11-11-05).
 *
 * This rule flags a native <code> or <pre> with a `{…}` child whose value is
 * named like a secret (identity, secret, token, recoveryCodes, password,
 * apiKey…, as an identifier, the last property of a member expression, or an
 * argument of a call / a part of a template), unless the element, or one of
 * its JSX ancestors, is a <SecretText> or carries a `ph-no-capture` class
 * (literally, or through NO_CAPTURE_CLASS). Render the value with
 * `<SecretText>` from "@/components/ui/secret-text" instead.
 *
 * Only files under src/ are checked.
 */
const SECRET_NAME = /(identity|secret|token|recoverycodes?|password|passphrase|apikey|privatekey)s?$/i;
const GUARDED = new Set(["code", "pre"]);

function inScope(filename) {
  return /(^|[\\/])src[\\/]/.test(filename);
}

/** Names an expression is read through, for the secret-name test. */
function namesOf(node) {
  if (!node) return [];
  switch (node.type) {
    case "Identifier":
      return [node.name];
    case "MemberExpression":
      return !node.computed && node.property.type === "Identifier" ? [node.property.name] : [];
    case "ChainExpression":
    case "TSNonNullExpression":
    case "TSAsExpression":
      return namesOf(node.expression);
    case "CallExpression":
      return node.arguments.flatMap(namesOf);
    case "TemplateLiteral":
      return node.expressions.flatMap(namesOf);
    case "BinaryExpression":
    case "LogicalExpression":
      return [...namesOf(node.left), ...namesOf(node.right)];
    case "ConditionalExpression":
      return [...namesOf(node.consequent), ...namesOf(node.alternate)];
    default:
      return [];
  }
}

function elementName(opening) {
  return opening.name.type === "JSXIdentifier" ? opening.name.name : null;
}

function blocksCapture(opening) {
  if (elementName(opening) === "SecretText") return true;
  return opening.attributes.some((a) => {
    if (a.type !== "JSXAttribute" || a.name.type !== "JSXIdentifier" || a.name.name !== "className") return false;
    const value = a.value;
    if (!value) return false;
    if (value.type === "Literal") return String(value.value).split(/\s+/).includes("ph-no-capture");
    if (value.type === "JSXExpressionContainer") {
      const text = JSON.stringify(value.expression, (key, v) => (key === "parent" ? undefined : v));
      return text.includes("ph-no-capture") || text.includes("NO_CAPTURE_CLASS");
    }
    return false;
  });
}

function guardedByAncestor(node) {
  for (let parent = node.parent; parent; parent = parent.parent) {
    if (parent.type === "JSXElement" && blocksCapture(parent.openingElement)) return true;
  }
  return false;
}

/** @type {import("eslint").Rule.RuleModule} */
export default {
  meta: {
    type: "problem",
    docs: { description: "Render secrets shown as text through <SecretText>" },
    schema: [],
    messages: {
      secret:
        "<{{element}}> renders `{{name}}`, which looks like a secret: use <SecretText> from \"@/components/ui/secret-text\" so session replay and autocapture never record it.",
    },
  },
  create(context) {
    if (!inScope(context.filename)) return {};
    return {
      JSXElement(node) {
        const element = elementName(node.openingElement);
        if (!element || !GUARDED.has(element)) return;
        if (blocksCapture(node.openingElement) || guardedByAncestor(node)) return;
        for (const child of node.children) {
          if (child.type !== "JSXExpressionContainer") continue;
          const name = namesOf(child.expression).find((n) => SECRET_NAME.test(n));
          if (name) context.report({ node: child, messageId: "secret", data: { element, name } });
        }
      },
    };
  },
};
