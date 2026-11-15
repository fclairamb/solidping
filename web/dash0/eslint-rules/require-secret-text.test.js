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
    // Derived values (spec 2026-11-15-04): guarded, truncated, or not text.
    { filename: src, code: 'const token = c.config.token; const url = `x?token=${token}`; const a = <SecretText as="span">{url}</SecretText>;' },
    { filename: src, code: "const token = c.config.token; const url = `x?token=${token}`; const a = <div className={NO_CAPTURE_CLASS}><span>{url}</span><a href={url}>x</a></div>;" },
    { filename: src, code: "const token = c.config.token; const short = `${token.slice(0, 8)}…`; const a = <span>{short}</span>;" },
    { filename: src, code: "const hasKey = privateKeys.includes(\"k\"); const stored = p.secret ?? false; const a = <div>{hasKey && stored && <b />}</div>;" },
    { filename: src, code: "const token = c.config.token; const url = `x?token=${token}`; const a = <button onClick={() => copy(url)}>copy</button>;" },
    { filename: src, code: "const token = c.config.token; const a = <Comp value={token} title={token} />;" },
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
    // Derived values (spec 2026-11-15-04).
    {
      filename: src,
      code: "const token = check.config?.token; const url = `${o}/hb?token=${token}`; const curl = `curl \"${url}\"`; const a = <div><span>{url}</span><span>{curl}</span></div>;",
      errors: [{ messageId: "derived" }, { messageId: "derived" }],
    },
    {
      filename: src,
      code: "const token = check.config?.token; const address = emailCheckAddress(token, domain); const a = <span title={address}>{address}</span>;",
      errors: [{ messageId: "derivedAttribute" }, { messageId: "derived" }],
    },
    {
      filename: src,
      code: "const token = check.config?.token; const mailto = `mailto:${token}@x`; const a = <a href={mailto}>send</a>;",
      errors: [{ messageId: "derivedAttribute" }],
    },
    {
      filename: src,
      code: "const token = check.config?.token; const upper = token.toUpperCase(); const a = <code>{upper}</code>;",
      errors: [{ messageId: "derived" }],
    },
  ],
});
