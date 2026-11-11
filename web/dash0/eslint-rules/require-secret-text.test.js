import { describe, it } from "vitest";
import { RuleTester } from "eslint";
import rule from "./require-secret-text.js";

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

const src = "src/components/a.tsx";

tester.run("require-secret-text", rule, {
  valid: [
    { filename: src, code: "const a = <SecretText>{generated.identity}</SecretText>;" },
    { filename: src, code: 'const a = <SecretText as="pre">{agentEnvLine(generated.identity)}</SecretText>;' },
    { filename: src, code: "const a = <code>{region.region}</code>;" },
    { filename: src, code: "const a = <code>{token.region}</code>;" },
    { filename: src, code: "const a = <code>{agent.fingerprint}</code>;" },
    { filename: src, code: 'const a = <code>{t("tokens.secretHint")}</code>;' },
    { filename: src, code: 'const a = <code className="ph-no-capture">{issuedSecret}</code>;' },
    { filename: src, code: "const a = <div className={NO_CAPTURE_CLASS}><code>{minted.token}</code></div>;" },
    { filename: src, code: "const a = <SecretText as=\"div\"><pre>{secret}</pre></SecretText>;" },
    // Out of scope: not under src/.
    { filename: "e2e/a.tsx", code: "const a = <code>{secret}</code>;" },
  ],
  invalid: [
    { filename: src, code: "const a = <code>{generated.identity}</code>;", errors: [{ messageId: "secret" }] },
    { filename: src, code: "const a = <pre>{agentEnvLine(generated.identity)}</pre>;", errors: [{ messageId: "secret" }] },
    { filename: src, code: "const a = <code>{createdToken.token}</code>;", errors: [{ messageId: "secret" }] },
    { filename: src, code: "const a = <code>{issuedSecret}</code>;", errors: [{ messageId: "secret" }] },
    { filename: src, code: "const a = <pre>{`TOKEN=${mintedToken}`}</pre>;", errors: [{ messageId: "secret" }] },
    { filename: src, code: "const a = <div>{recoveryCodes.map((c) => <code>{c}</code>)}<code>{recoveryCodes}</code></div>;", errors: [{ messageId: "secret" }] },
    { filename: src, code: 'const a = <code className="text-xs">{minted?.token}</code>;', errors: [{ messageId: "secret" }] },
  ],
});
