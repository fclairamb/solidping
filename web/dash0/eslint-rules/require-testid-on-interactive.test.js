import { describe, it } from "vitest";
import { RuleTester } from "eslint";
import rule from "./require-testid-on-interactive.js";

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

const src = "src/routes/a.tsx";

tester.run("require-testid-on-interactive", rule, {
  valid: [
    { filename: src, code: 'const a = <Button data-testid="save-button">x</Button>;' },
    { filename: src, code: "const a = <Button {...props}>x</Button>;" },
    { filename: src, code: "const a = <div onClick={go}>x</div>;" },
    { filename: src, code: "const a = <span>x</span>;" },
    // Out of scope: not under src/.
    { filename: "e2e/a.tsx", code: "const a = <Button>x</Button>;" },
  ],
  invalid: [
    { filename: src, code: "const a = <Button>Save</Button>;", errors: [{ messageId: "missing" }] },
    { filename: src, code: "const a = <input />;", errors: [{ messageId: "missing" }] },
    { filename: src, code: 'const a = <Link to="/x">x</Link>;', errors: [{ messageId: "missing" }] },
    { filename: src, code: "const a = <form onSubmit={f} />;", errors: [{ messageId: "missing" }] },
    { filename: src, code: "const a = <DialogContent>x</DialogContent>;", errors: [{ messageId: "missing" }] },
  ],
});
