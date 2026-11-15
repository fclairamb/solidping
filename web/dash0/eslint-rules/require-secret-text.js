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
 * It also tracks values derived from a secret (spec 2026-11-15-04): a
 * variable whose initializer reads a secret-named value, or another derived
 * variable, is derived too (`const url = `…?token=${token}``, then
 * `const curl = `curl "${url}"``). A derived variable is reported wherever a
 * native element renders it: as a child of any element (a <span>, a <div>…),
 * or in an attribute session replay records with the DOM (title, href,
 * aria-label, alt, placeholder). The heartbeat URL and the email check address
 * slipped through that way, named `heartbeatUrl` and `address`. Variables are
 * matched by name within the file, which is coarse but errs on the safe side.
 *
 * Only files under src/ are checked.
 */
const SECRET_NAME = /(identity|secret|token|recoverycodes?|password|passphrase|apikey|privatekey)s?$/i;
const GUARDED = new Set(["code", "pre"]);
// Methods whose result still holds the receiver's text, and calls that never
// return a string. `slice` / `substring` are left out on purpose: the checks
// list shows the first 8 characters of the email token, not the token.
const STRING_METHODS = new Set(["toUpperCase", "toLowerCase", "trim", "replace", "replaceAll", "concat", "padStart", "padEnd", "toString", "join"]);
const NON_STRING_CALLS = new Set(["Boolean", "Number", "isNaN"]);
const RECORDED_ATTRIBUTES = new Set(["title", "href", "aria-label", "alt", "placeholder"]);

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

/**
 * The names a string value is built from: what a template, a concatenation,
 * a call's arguments or a string method's receiver read. A comparison, a
 * negation, a `cond && …` guard or a method that returns something else
 * (`includes`, `length`, a prefix `slice`) carries no secret text, so it is
 * not followed.
 */
function builtFrom(node) {
  if (!node) return [];
  switch (node.type) {
    case "Identifier":
      return [node.name];
    case "MemberExpression":
      return !node.computed && node.property.type === "Identifier" ? [node.property.name] : [];
    case "ChainExpression":
    case "TSNonNullExpression":
    case "TSAsExpression":
    case "AwaitExpression":
      return builtFrom(node.expression);
    case "CallExpression": {
      const callee = node.callee;
      const method = callee.type === "MemberExpression" && !callee.computed ? callee.property.name : undefined;
      if (method && STRING_METHODS.has(method)) return [...builtFrom(callee.object), ...node.arguments.flatMap(builtFrom)];
      if (callee.type === "Identifier" && NON_STRING_CALLS.has(callee.name)) return [];
      return node.arguments.flatMap(builtFrom);
    }
    case "TemplateLiteral":
      return node.expressions.flatMap(builtFrom);
    case "BinaryExpression":
      return node.operator === "+" ? [...builtFrom(node.left), ...builtFrom(node.right)] : [];
    case "LogicalExpression":
      return node.operator === "&&" ? builtFrom(node.right) : [...builtFrom(node.left), ...builtFrom(node.right)];
    case "ConditionalExpression":
      return [...builtFrom(node.consequent), ...builtFrom(node.alternate)];
    case "ArrayExpression":
      return node.elements.flatMap(builtFrom);
    default:
      return [];
  }
}

/** Names of the variables of the file whose value is derived from a secret. */
function derivedNames(program) {
  const declarators = [];
  const visit = (node) => {
    if (!node || typeof node.type !== "string") return;
    if (node.type === "VariableDeclarator" && node.id.type === "Identifier" && node.init) declarators.push(node);
    for (const key of Object.keys(node)) {
      if (key === "parent") continue;
      const child = node[key];
      if (Array.isArray(child)) child.forEach(visit);
      else if (child && typeof child.type === "string") visit(child);
    }
  };
  visit(program);
  const derived = new Set();
  let changed = true;
  while (changed) {
    changed = false;
    for (const d of declarators) {
      if (derived.has(d.id.name)) continue;
      if (builtFrom(d.init).some((n) => SECRET_NAME.test(n) || derived.has(n))) {
        derived.add(d.id.name);
        changed = true;
      }
    }
  }
  return derived;
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
      derived:
        "<{{element}}> renders `{{name}}`, which is built from a secret: use <SecretText> from \"@/components/ui/secret-text\" (or a `ph-no-capture` ancestor) so session replay never records it.",
      derivedAttribute:
        "<{{element}} {{attribute}}> carries `{{name}}`, which is built from a secret: session replay records DOM attributes, so drop it or put the element under `ph-no-capture`.",
    },
  },
  create(context) {
    if (!inScope(context.filename)) return {};
    let derived = new Set();
    return {
      Program(program) {
        derived = derivedNames(program);
      },
      JSXElement(node) {
        const element = elementName(node.openingElement);
        // Native elements only: a component decides itself what it renders.
        if (!element || !/^[a-z]/.test(element)) return;
        if (blocksCapture(node.openingElement) || guardedByAncestor(node)) return;
        for (const child of node.children) {
          if (child.type !== "JSXExpressionContainer") continue;
          if (GUARDED.has(element)) {
            const name = namesOf(child.expression).find((n) => SECRET_NAME.test(n));
            if (name) {
              context.report({ node: child, messageId: "secret", data: { element, name } });
              continue;
            }
          }
          const name = builtFrom(child.expression).find((n) => derived.has(n));
          if (name) context.report({ node: child, messageId: "derived", data: { element, name } });
        }
        for (const attr of node.openingElement.attributes) {
          if (attr.type !== "JSXAttribute" || attr.name.type !== "JSXIdentifier") continue;
          if (!RECORDED_ATTRIBUTES.has(attr.name.name) || attr.value?.type !== "JSXExpressionContainer") continue;
          const name = builtFrom(attr.value.expression).find((n) => derived.has(n));
          if (name) {
            context.report({
              node: attr,
              messageId: "derivedAttribute",
              data: { element, attribute: attr.name.name, name },
            });
          }
        }
      },
    };
  },
};
