import globals from "globals";

/**
 * ESLint rule: no-node-scope-in-browser-callback
 *
 * Playwright serializes the callback passed to `evaluate`, `evaluateHandle`,
 * `$eval`, `$$eval`, `waitForFunction` and `addInitScript` and runs it **in the
 * browser**. Anything the callback closes over lives in the Node process and is
 * simply not there at runtime — the page throws `ReferenceError: X is not
 * defined`. Neither ESLint's stock rules nor `tsc` can see it: closing over an
 * in-scope module import is perfectly legal TypeScript.
 *
 * That is exactly how spec 2026-09-09-01's mechanical base-path sweep shipped
 *
 *     page.evaluate(() => navigator.serviceWorker.getRegistration(`${DASH_BASE}/sw.js`))
 *
 * with `DASH_BASE` imported from `./fixtures` (fixed in 1dca14ba8 by passing the
 * value as the `evaluate` argument). This rule is the guard for that class of
 * bug — see spec 2026-09-09-03.
 *
 * It does real scope analysis rather than an esquery pattern match: for each
 * such callback it takes the callback's own scope and inspects `scope.through`,
 * the references that could not be resolved anywhere inside the callback.
 *
 *   - Allowed: the callback's own parameters and locals (they never reach
 *     `through`), and browser globals (`window`, `document`, `navigator`,
 *     `localStorage`, `self`, `fetch`, … — the full `globals.browser` set, plus
 *     any identifier ESLint cannot resolve at all).
 *   - Flagged: an identifier that resolves to a binding *declared outside* the
 *     callback — an import, a module-level `const`, an enclosing test's local.
 *
 * Type-only references are ignored: `el as HTMLInputElement` and
 * `import type { Page }` are erased before the callback is ever serialized.
 *
 * Receivers are not constrained. In an `e2e/` tree these six method names are
 * Playwright's, whatever they are called on — `page`, `context`, a `locator`,
 * or a chained `page.getByTestId(…)` — and pattern-matching the receiver name
 * would miss the chained and aliased forms that actually appear in the suites.
 *
 * NOTE: this file is duplicated verbatim in web/dash0 and web/status0. They are
 * independent Bun packages with their own node_modules and their own flat
 * config; the existing e2e `no-restricted-syntax` guard is duplicated the same
 * way. Keep the two copies in sync.
 */

// Method name -> index of the argument that carries the browser-side callback.
const BROWSER_CALLBACK_ARG = {
  evaluate: 0,
  evaluateHandle: 0,
  waitForFunction: 0,
  addInitScript: 0,
  // (selector, pageFunction, arg)
  $eval: 1,
  $$eval: 1,
};

const BROWSER_GLOBALS = new Set(Object.keys(globals.browser));

/** @type {import("eslint").Rule.RuleModule} */
const rule = {
  meta: {
    type: "problem",
    docs: {
      description:
        "Disallow closing over Node-side bindings inside a Playwright browser callback",
    },
    schema: [],
    messages: {
      nodeScope:
        "`{{name}}` is a Node-side binding, but this callback is serialized and " +
        "run in the browser, where it does not exist (ReferenceError at runtime). " +
        "Pass it in as the `{{method}}` argument instead: " +
        "`{{method}}((arg) => …, {{name}})`.",
    },
  },

  create(context) {
    const sourceCode = context.sourceCode ?? context.getSourceCode();

    function isFunctionNode(node) {
      return (
        node != null &&
        (node.type === "ArrowFunctionExpression" ||
          node.type === "FunctionExpression")
      );
    }

    return {
      CallExpression(node) {
        const callee = node.callee;
        if (
          callee.type !== "MemberExpression" ||
          callee.computed ||
          callee.property.type !== "Identifier"
        ) {
          return;
        }

        const method = callee.property.name;
        if (!Object.hasOwn(BROWSER_CALLBACK_ARG, method)) return;

        const fn = node.arguments[BROWSER_CALLBACK_ARG[method]];
        if (!isFunctionNode(fn)) return;

        const scope = sourceCode.getScope(fn);
        // `through` holds every reference made inside this callback (at any
        // nesting depth) that no scope within the callback could resolve.
        for (const ref of scope.through) {
          const identifier = ref.identifier;
          if (!identifier || identifier.type !== "Identifier") continue;

          // Types are erased; only value references are shipped to the browser.
          if (ref.isTypeReference && !ref.isValueReference) continue;

          const name = identifier.name;
          if (BROWSER_GLOBALS.has(name)) continue;

          const resolved = ref.resolved;
          // Unresolved: an ambient/browser global ESLint does not know about.
          // Resolved with no definition: a declared global (globals.browser et al).
          if (!resolved || resolved.defs.length === 0) continue;

          context.report({
            node: identifier,
            messageId: "nodeScope",
            data: { name, method },
          });
        }
      },
    };
  },
};

export default rule;
