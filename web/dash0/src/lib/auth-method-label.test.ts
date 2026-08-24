import { describe, expect, it } from "vitest";
import type { TFunction } from "i18next";

import { methodLabel } from "@/lib/auth-method-label";

import accountEn from "@/locales/en/account.json";

/**
 * A `t` backed by the real en account bundle, resolving dotted keys the way
 * i18next does — and, crucially, returning the KEY on a miss, exactly as
 * i18next would. That is what makes the "unknown method" tests meaningful: a
 * helper that blindly called t() would leak `sessions.methodFoo` into the UI.
 */
function tFor(bundle: unknown): TFunction {
  const resolve = (key: string): string => {
    const parts = key.split(".");
    let node: unknown = bundle;
    for (const part of parts) {
      if (typeof node !== "object" || node === null || !(part in node)) return key;
      node = (node as Record<string, unknown>)[part];
    }

    return typeof node === "string" ? node : key;
  };

  return resolve as unknown as TFunction;
}

const t = tFor(accountEn);

// The full authMethods() set server/internal/handlers/auth/audit.go can
// emit, paired with the label the en bundle carries for each.
const KNOWN_METHODS: Record<string, string> = {
  password: "Password",
  ldap: "LDAP",
  passkey: "Passkey",
  oauth: "OAuth",
  google: "Google",
  github: "GitHub",
  gitlab: "GitLab",
  microsoft: "Microsoft",
  discord: "Discord",
  slack: "Slack",
  oidc: "OIDC",
  saml: "SAML",
  invitation: "Invitation",
  registration: "Registration",
  switch_org: "Switch organization",
  org_session: "Organization session",
};

describe("methodLabel", () => {
  it("returns null when there is no method at all", () => {
    expect(methodLabel(undefined, t)).toBeNull();
  });

  it("labels every base auth method the backend can emit", () => {
    for (const [method, want] of Object.entries(KNOWN_METHODS)) {
      expect(methodLabel(method, t)).toBe(want);
    }
  });

  it("labels a +totp second factor without losing the first factor", () => {
    expect(methodLabel("password+totp", t)).toBe("Password + TOTP");
    expect(methodLabel("ldap+totp", t)).toBe("LDAP + TOTP");
  });

  it("labels a +recovery_code second factor", () => {
    expect(methodLabel("password+recovery_code", t)).toBe("Password + Recovery code");
  });

  it("never returns no badge for an unrecognized non-empty method", () => {
    const label = methodLabel("some_new_idp", t);
    expect(label).not.toBeNull();
    expect(label).not.toBe("");
    // Must not leak a raw i18n key into the UI.
    expect(label).not.toContain("sessions.");
    // Falls back to a humanized form of the raw string.
    expect(label).toBe("Some New Idp");
  });

  it("humanizes an unrecognized second factor the same way", () => {
    const label = methodLabel("password+webauthn_step_up", t);
    expect(label).toBe("Password + Webauthn Step Up");
  });
});
