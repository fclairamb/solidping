import js from "@eslint/js";
import globals from "globals";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";
import tseslint from "typescript-eslint";
import noNodeScopeInBrowserCallback from "./eslint-rules/no-node-scope-in-browser-callback.js";

export default tseslint.config(
  { ignores: ["dist"] },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ["**/*.{ts,tsx}"],
    languageOptions: {
      ecmaVersion: 2020,
      globals: globals.browser,
    },
    plugins: {
      "react-hooks": reactHooks,
      "react-refresh": reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      "react-refresh/only-export-components": [
        "warn",
        { allowConstantExport: true },
      ],
    },
  },
  {
    // Regression guard for spec 2026-08-07-01: e2e specs must obtain their
    // server origin by importing API_BASE from ./fixtures, never by reading
    // process.env.E2E_API_BASE (an env var nothing sets — it silently
    // reintroduces a hardcoded localhost:4000 fallback) or by hardcoding
    // http://localhost:4000 themselves. fixtures.ts itself is exempt since
    // it's the one file allowed to define these values.
    files: ["e2e/**/*.ts"],
    ignores: ["e2e/fixtures.ts"],
    rules: {
      "no-restricted-syntax": [
        "error",
        {
          selector:
            "MemberExpression[object.type='MemberExpression'][object.object.name='process'][object.property.name='env'][property.name='E2E_API_BASE']",
          message:
            "E2E_API_BASE is dead configuration that nothing sets. Import API_BASE from ./fixtures instead (derived from E2E_BASE_URL).",
        },
        {
          selector: "Literal[value=/localhost:4000/]",
          message:
            "Do not hardcode localhost:4000 in e2e specs. Import API_BASE from ./fixtures instead (derived from E2E_BASE_URL).",
        },
        {
          selector: "TemplateElement[value.raw=/localhost:4000/]",
          message:
            "Do not hardcode localhost:4000 in e2e specs. Import API_BASE from ./fixtures instead (derived from E2E_BASE_URL).",
        },
      ],
    },
  },
  {
    // Playwright serializes the callback passed to evaluate / evaluateHandle /
    // $eval / $$eval / waitForFunction / addInitScript and runs it in the page.
    // Closing over a Node-side binding (a module import, a test local) is legal
    // TypeScript that throws a ReferenceError in the browser — that is exactly
    // how the DASH_BASE bug fixed in 1dca14ba8 shipped green. This rule does the
    // scope analysis that an esquery `no-restricted-syntax` selector cannot;
    // see the rule's own header (spec 2026-09-09-03).
    //
    // Unlike the block above, fixtures.ts is NOT exempt: it drives the page too.
    files: ["e2e/**/*.ts"],
    plugins: {
      // Local, in-config plugin — no new npm package.
      "e2e-local": {
        rules: {
          "no-node-scope-in-browser-callback": noNodeScopeInBrowserCallback,
        },
      },
    },
    rules: {
      "e2e-local/no-node-scope-in-browser-callback": "error",
    },
  }
);
