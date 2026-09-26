import { describe, it } from "vitest";
import { RuleTester } from "eslint";
import rule from "./no-untranslated-jsx-text.js";

// Positive and negative controls for the JSX literal guard (spec
// 2026-09-26-01): a rule that reports nothing on src/ proves nothing unless it
// is shown to report the cases it exists for.
RuleTester.describe = describe;
RuleTester.it = it;
RuleTester.itOnly = it.only;

const tester = new RuleTester({
  languageOptions: {
    ecmaVersion: 2022,
    sourceType: "module",
    parserOptions: { ecmaFeatures: { jsx: true } },
  },
});

tester.run("no-untranslated-jsx-text", rule, {
  valid: [
    { code: 'const a = <Button>{t("clearFilters")}</Button>;' },
    { code: "const a = <span>—</span>;" },
    { code: "const a = <span>&gt; {count}</span>;" },
    { code: 'const a = <Input placeholder="example.com" />;' },
    { code: 'const a = <img alt={t("screenshot_alt")} />;' },
    { code: "const a = <span>ms</span>;", options: [{ allow: ["ms"] }] },
    { code: "const a = <b> SolidPing </b>;", options: [{ allow: ["SolidPing"] }] },
  ],
  invalid: [
    { code: "const a = <Button>Clear filters</Button>;", errors: [{ messageId: "literal" }] },
    { code: 'const a = <span>{"Needs Action"}</span>;', errors: [{ messageId: "literal" }] },
    { code: 'const a = <img alt="Screenshot" />;', errors: [{ messageId: "literal" }] },
    { code: 'const a = <button aria-label={"Close"} />;', errors: [{ messageId: "literal" }] },
    { code: 'const a = <MetaRow label="Created" />;', errors: [{ messageId: "literal" }] },
    { code: "const a = <>Used as tunnel by {n} checks</>;", errors: [{ messageId: "literal" }, { messageId: "literal" }] },
    // The allow list is exact: a sentence containing an allowed word is still flagged.
    { code: "const a = <p>Powered by SolidPing</p>;", options: [{ allow: ["SolidPing"] }], errors: [{ messageId: "literal" }] },
  ],
});
