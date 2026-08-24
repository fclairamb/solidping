// Human-readable labels for the auth_method values a session's
// createdWith.method can carry — the full set authMethods() in
// server/internal/handlers/auth/audit.go can emit, plus the "<base>+totp" /
// "<base>+recovery_code" second-factor suffixes withSecondFactor appends.
//
// Unknown values must still render something on the sessions page badge —
// never silently drop it — so a method or second factor this frontend
// doesn't have a translation for yet falls back to a humanized form of the
// raw string instead of vanishing.

const BASE_METHOD_KEYS: Record<string, string> = {
  password: "sessions.methodPassword",
  ldap: "sessions.methodLdap",
  passkey: "sessions.methodPasskey",
  oauth: "sessions.methodOauth",
  google: "sessions.methodGoogle",
  github: "sessions.methodGithub",
  gitlab: "sessions.methodGitlab",
  microsoft: "sessions.methodMicrosoft",
  discord: "sessions.methodDiscord",
  slack: "sessions.methodSlack",
  oidc: "sessions.methodOidc",
  saml: "sessions.methodSaml",
  invitation: "sessions.methodInvitation",
  registration: "sessions.methodRegistration",
  switch_org: "sessions.methodSwitchOrg",
  org_session: "sessions.methodOrgSession",
};

const SECOND_FACTOR_KEYS: Record<string, string> = {
  totp: "sessions.secondFactorTotp",
  recovery_code: "sessions.secondFactorRecoveryCode",
};

// humanize turns an unrecognized raw value like "some_new_idp" into
// "Some New Idp" rather than showing nothing or the raw snake_case.
function humanize(raw: string): string {
  return raw
    .split(/[_-]+/)
    .filter(Boolean)
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(" ");
}

/**
 * methodLabel renders the badge text for a session's createdWith.method.
 * Returns null only when there is no method to show at all — a non-empty
 * method, known or not, always produces a readable label.
 */
export function methodLabel(method: string | undefined, t: (key: string) => string): string | null {
  if (!method) return null;

  const [base, secondFactor] = method.split("+");
  const baseLabel = BASE_METHOD_KEYS[base] ? t(BASE_METHOD_KEYS[base]) : humanize(base);

  if (!secondFactor) return baseLabel;

  const factorLabel = SECOND_FACTOR_KEYS[secondFactor]
    ? t(SECOND_FACTOR_KEYS[secondFactor])
    : humanize(secondFactor);

  return `${baseLabel} + ${factorLabel}`;
}
