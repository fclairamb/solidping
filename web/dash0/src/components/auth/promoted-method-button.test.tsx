import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

import { PromotedMethodButton } from "./promoted-method-button";

// solidping-business#32: in French ("Se connecter avec une clé d'accès" +
// "Utilisé en dernier") the promoted passkey button overflowed the login card
// on a phone, because the base Button never wraps and has a fixed height.
describe("PromotedMethodButton", () => {
  const html = renderToStaticMarkup(
    <PromotedMethodButton
      icon={<svg />}
      label="Se connecter avec une clé d'accès"
      badge="Utilisé en dernier"
      onClick={() => {}}
      data-testid="passkey-login-button-promoted"
    />,
  );
  const button = /<button[^>]*>/.exec(html)?.[0] ?? "";
  const classes = (/class="([^"]*)"/.exec(button)?.[1] ?? "").split(/\s+/);

  it("lets a long translated label wrap instead of overflowing", () => {
    expect(classes).toContain("whitespace-normal");
    expect(classes).not.toContain("whitespace-nowrap");
    expect(classes).toContain("h-auto");
    expect(classes).not.toContain("h-9");
    expect(classes).toContain("flex-wrap");
  });

  it("renders the label, the badge and the test ids", () => {
    expect(button).toContain('data-testid="passkey-login-button-promoted"');
    expect(html).toContain('data-testid="login-last-used-badge"');
    expect(html).toContain("Utilisé en dernier");
  });
});
