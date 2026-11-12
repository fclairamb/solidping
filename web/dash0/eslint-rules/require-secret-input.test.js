import { describe, it } from "vitest";
import { RuleTester } from "eslint";
import rule from "./require-secret-input.js";

RuleTester.describe = describe;
RuleTester.it = it;

const tester = new RuleTester({
  languageOptions: { parserOptions: { ecmaFeatures: { jsx: true } } },
});
const filename = "/repo/web/dash0/src/components/x.tsx";

tester.run("require-secret-input", rule, {
  valid: [
    { filename, code: `const a = <Textarea id="kubeconfig" data-secret-input />;` },
    { filename, code: `const a = <PasswordInput id="password" />;` },
    { filename, code: `const a = <Input id="name" data-testid="integration-name" />;` },
  ],
  invalid: [
    { filename, code: `const a = <Textarea id="kubeconfig" />;`, errors: [{ messageId: "missing" }] },
    { filename, code: `const a = <Input data-testid="webhook-url" />;`, errors: [{ messageId: "missing" }] },
    { filename, code: "const a = <Input data-testid={`provider-${k}-token`} />;", errors: [{ messageId: "missing" }] },
  ],
});
