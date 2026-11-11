/**
 * @vitest-environment jsdom
 *
 * GHSA-c9qq-4m99-c7f6 (spec 2026-11-11-05): dash0 records PostHog sessions
 * unmasked, so every shown-once secret rendered as text must sit under a
 * `ph-no-capture` element (posthog-js blocks it from replay and autocapture).
 * Light components are rendered; the heavy route screens are checked on
 * their source, next to the `secret-local/require-secret-text` ESLint rule.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { cleanup, render, screen } from "@testing-library/react";
import "@/i18n";

vi.mock("@/api/twofa", () => ({
  setup2FA: async () => ({ secret: "JBSWY3DPEHPK3PXP", uri: "", setupToken: "st" }),
  confirm2FA: async () => ({ recoveryCodes: [] }),
}));

import { RecoveryCodesDialog } from "./RecoveryCodesDialog";
import { TOTPSetupDialog } from "./TOTPSetupDialog";
import { CopyableCode, CopyableInline } from "@/components/shared/copyable-code";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { PasswordInput } from "@/components/ui/password-input";

afterEach(cleanup);

function blocked(element: Element): boolean {
  return element.closest(".ph-no-capture") !== null;
}

describe("shown-once secrets rendered as text", () => {
  it("TOTP setup: the manual secret is blocked", async () => {
    render(<TOTPSetupDialog open hasPassword={false} onClose={() => {}} onComplete={() => {}} />);
    const secret = await screen.findByTestId("2fa-manual-secret");
    expect((secret as HTMLInputElement).value).toBe("JBSWY3DPEHPK3PXP");
    expect(blocked(secret)).toBe(true);
  });

  it("recovery codes are blocked", () => {
    render(<RecoveryCodesDialog open codes={["aaaa-bbbb", "cccc-dddd"]} onClose={() => {}} />);
    const codes = screen.getByTestId("2fa-recovery-codes");
    expect(codes.textContent).toContain("aaaa-bbbb");
    expect(blocked(codes)).toBe(true);
  });

  it("CopyableCode and CopyableInline block a secret value, and only then", () => {
    render(
      <>
        <CopyableCode secret code="SP_AGENT_ENROLLMENT_TOKEN=spe_x" data-testid="secret-code" />
        <CopyableCode code="https://example.com" data-testid="plain-code" />
        <div data-testid="secret-inline">
          <CopyableInline secret value="spk_kiosk" />
        </div>
      </>,
    );
    expect(blocked(screen.getByTestId("secret-code").querySelector("code")!)).toBe(true);
    expect(blocked(screen.getByTestId("plain-code").querySelector("code")!)).toBe(false);
    expect(blocked(screen.getByTestId("secret-inline").querySelector("code")!)).toBe(true);
  });

  it("text-type inputs marked data-secret-input are blocked, plain ones are not", () => {
    render(
      <>
        <Input type="text" data-secret-input data-testid="secret-input" />
        <Textarea data-secret-input data-testid="secret-textarea" />
        <Input type="text" data-testid="plain-input" />
        <PasswordInput data-testid="password-input" />
      </>,
    );
    expect(blocked(screen.getByTestId("secret-input"))).toBe(true);
    expect(blocked(screen.getByTestId("secret-textarea"))).toBe(true);
    expect(blocked(screen.getByTestId("plain-input"))).toBe(false);
    // Revealing a password turns it into plain text: always blocked.
    expect(blocked(screen.getByTestId("password-input"))).toBe(true);
  });
});

// The opening tag carrying `data-testid="<id>"` in a source file.
function openingTag(source: string, testId: string): string {
  const at = source.indexOf(`data-testid="${testId}"`);
  expect(at, `data-testid="${testId}" not found`).toBeGreaterThan(-1);
  const start = source.lastIndexOf("<", at);
  return source.slice(start, source.indexOf(">", at) + 1);
}

const src = join(__dirname, "..", "..");
const screens: { file: string; testId: string }[] = [
  { file: "routes/orgs/$org/account.tokens.tsx", testId: "token-created-value" },
  { file: "routes/orgs/$org/organization.private-locations.index.tsx", testId: "minted-token" },
  { file: "routes/orgs/$org/organization.private-locations.index.tsx", testId: "docker-run-command" },
  { file: "routes/orgs/$org/organization.private-locations.register.tsx", testId: "wizard-minted-token" },
  { file: "routes/orgs/$org/organization.private-locations.register.tsx", testId: "wizard-snippet-docker-run" },
  { file: "routes/orgs/$org/organization.private-locations.register.tsx", testId: "wizard-snippet-docker-compose" },
  { file: "routes/orgs/$org/organization.private-locations.register.tsx", testId: "wizard-snippet-kubernetes" },
  { file: "components/integrations/integration-form.tsx", testId: "webhook-signing-secret" },
  { file: "components/integrations/integration-form.tsx", testId: "msteams-bot-link-code" },
  { file: "components/shared/status-page-subscribers.tsx", testId: "subscriber-issued-secret-value" },
  { file: "components/shared/status-page-subscribers.tsx", testId: "subscriber-secret-input" },
  { file: "components/shared/region-key-cell.tsx", testId: "region-key-secret" },
  { file: "components/shared/region-key-cell.tsx", testId: "region-key-env-line" },
];

describe("route screens render their shown-once secret blocked", () => {
  it.each(screens)("$file: $testId", ({ file, testId }) => {
    const tag = openingTag(readFileSync(join(src, file), "utf8"), testId);
    const ok =
      tag.startsWith("<SecretText") ||
      (tag.startsWith("<CopyableCode") && /\bsecret\b/.test(tag)) ||
      tag.includes("NO_CAPTURE_CLASS");
    expect(ok, tag).toBe(true);
  });

  it("the status page TV token passes `secret` to CopyableInline", () => {
    const source = readFileSync(join(src, "components/shared/status-page-tv-card.tsx"), "utf8");
    expect(source).toMatch(/<CopyableInline\s+value=\{mintedToken\}[^>]*\bsecret\b/);
  });
});
