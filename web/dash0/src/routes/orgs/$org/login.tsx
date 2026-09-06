import { useState, useEffect, useCallback, useRef } from "react";
import {
  createFileRoute,
  Link,
  useNavigate,
  useSearch,
} from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { useAuth, type OrganizationSummary, type LoginResult } from "@/contexts/AuthContext";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { PasswordInput } from "@/components/ui/password-input";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Logo } from "@/components/ui/logo";
import { AuthSplitLayout } from "@/components/layout/auth-split-layout";
import {
  AlertCircle,
  KeyRound,
  Loader2,
  Building2,
  PlayCircle,
} from "lucide-react";
import { ApiError } from "@/api/client";
import { useVersion, useProviders } from "@/api/hooks";
import { useDemoConfig } from "@/api/public-config";
import {
  getLastAuthMethod,
  setLastAuthMethod,
} from "@/lib/last-auth-method";
import {
  startAuthentication,
  browserSupportsWebAuthn,
  browserSupportsWebAuthnAutofill,
} from "@simplewebauthn/browser";
import {
  beginPasskeyLogin,
  finishPasskeyLogin,
  getAuthProviders,
} from "@/api/passkeys";
import { classifyPasskeyError } from "@/lib/passkey-error";
import {
  isOAuthAuthorizeReturnTo,
  resolveDestination,
  stripOAuthErrorParams,
  type LoginDestination,
} from "@/lib/login-destination";
import { refreshAccessToken } from "@/lib/token-refresh";
import { CHANGELOG_URL, marketingSiteUrl } from "@/lib/marketing-url";

// App base path (build-time constant). `returnTo` values captured on the way
// into /login already include it, so the destination resolver matches against
// `${BASE_PATH}/orgs/`.
const BASE_PATH = import.meta.env.VITE_BASE_URL || "";

export const Route = createFileRoute("/orgs/$org/login")({
  validateSearch: (
    search: Record<string, unknown>,
  ): { session_expired: boolean; returnTo?: string; demo?: boolean } => ({
    // TanStack Router's default search parser already coerces "true"/"false"
    // query-string values to native booleans before validateSearch runs, so
    // a bare `=== "true"` string comparison silently always evaluates to
    // false — the same bug class already worked around in jobs.*.tsx's
    // `allOrgs` param. Without this, a full-page redirect to
    // `?session_expired=true` (api/client.ts's redirectToExpiredLogin, used
    // by every escalating refresh failure) landed on the login page with
    // the "your session expired" banner silently suppressed.
    session_expired: search.session_expired === true || search.session_expired === "true",
    returnTo: typeof search.returnTo === "string" ? search.returnTo : undefined,
    // `?demo=1` signs the visitor straight into the shared live demo on load
    // (spec 2026-09-06-02), so the marketing site can deep-link into a working
    // dashboard rather than into a login form. Same coercion caveat as
    // session_expired above: TanStack already turned "true" into a boolean,
    // so a bare string comparison would silently never match.
    // Optional in the emitted type, deliberately: every existing
    // `navigate({ to: "/orgs/$org/login", search: … })` in the app predates
    // this param and must keep compiling without naming it.
    demo:
      search.demo === true ||
      search.demo === "true" ||
      search.demo === "1" ||
      search.demo === 1
        ? true
        : undefined,
  }),
  component: LoginPage,
});

function GoogleIcon({ className }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24">
      <path
        d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92a5.06 5.06 0 0 1-2.2 3.32v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.1z"
        fill="#4285F4"
      />
      <path
        d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z"
        fill="#34A853"
      />
      <path
        d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l2.85-2.22.81-.62z"
        fill="#FBBC05"
      />
      <path
        d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z"
        fill="#EA4335"
      />
    </svg>
  );
}

function SlackIcon({ className }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24">
      <path
        d="M5.042 15.165a2.528 2.528 0 0 1-2.52 2.523A2.528 2.528 0 0 1 0 15.165a2.527 2.527 0 0 1 2.522-2.52h2.52v2.52zm1.271 0a2.527 2.527 0 0 1 2.521-2.52 2.527 2.527 0 0 1 2.521 2.52v6.313A2.528 2.528 0 0 1 8.834 24a2.528 2.528 0 0 1-2.521-2.522v-6.313z"
        fill="#E01E5A"
      />
      <path
        d="M8.834 5.042a2.528 2.528 0 0 1-2.521-2.52A2.528 2.528 0 0 1 8.834 0a2.528 2.528 0 0 1 2.521 2.522v2.52H8.834zm0 1.271a2.528 2.528 0 0 1 2.521 2.521 2.528 2.528 0 0 1-2.521 2.521H2.522A2.528 2.528 0 0 1 0 8.834a2.528 2.528 0 0 1 2.522-2.521h6.312z"
        fill="#36C5F0"
      />
      <path
        d="M18.956 8.834a2.528 2.528 0 0 1 2.522-2.521A2.528 2.528 0 0 1 24 8.834a2.528 2.528 0 0 1-2.522 2.521h-2.522V8.834zm-1.27 0a2.528 2.528 0 0 1-2.523 2.521 2.527 2.527 0 0 1-2.52-2.521V2.522A2.527 2.527 0 0 1 15.163 0a2.528 2.528 0 0 1 2.523 2.522v6.312z"
        fill="#2EB67D"
      />
      <path
        d="M15.163 18.956a2.528 2.528 0 0 1 2.523 2.522A2.528 2.528 0 0 1 15.163 24a2.527 2.527 0 0 1-2.52-2.522v-2.522h2.52zm0-1.27a2.527 2.527 0 0 1-2.52-2.523 2.527 2.527 0 0 1 2.52-2.52h6.315A2.528 2.528 0 0 1 24 15.163a2.528 2.528 0 0 1-2.522 2.523h-6.315z"
        fill="#ECB22E"
      />
    </svg>
  );
}

function GitHubIcon({ className }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24">
      <path
        d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12"
        fill="currentColor"
      />
    </svg>
  );
}

function MicrosoftIcon({ className }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24">
      <path d="M0 0h11.377v11.377H0z" fill="#F25022" />
      <path d="M12.623 0H24v11.377H12.623z" fill="#7FBA00" />
      <path d="M0 12.623h11.377V24H0z" fill="#00A4EF" />
      <path d="M12.623 12.623H24V24H12.623z" fill="#FFB900" />
    </svg>
  );
}

function GitLabIcon({ className }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24">
      <path
        d="M23.955 13.587l-1.342-4.135-2.664-8.189a.455.455 0 0 0-.867 0L16.418 9.45H7.582L4.918 1.263a.455.455 0 0 0-.867 0L1.386 9.45.044 13.587a.924.924 0 0 0 .331 1.023L12 23.054l11.625-8.443a.92.92 0 0 0 .33-1.024"
        fill="#E24329"
      />
      <path d="M12 23.054L16.418 9.45H7.582z" fill="#FC6D26" />
      <path
        d="M12 23.054l-4.418-13.6H1.386z"
        fill="#FCA326"
      />
      <path
        d="M1.386 9.45L.044 13.587a.924.924 0 0 0 .331 1.023L12 23.054z"
        fill="#E24329"
      />
      <path
        d="M1.386 9.452h6.196L4.918 1.263a.455.455 0 0 0-.867 0z"
        fill="#FC6D26"
      />
      <path
        d="M12 23.054l4.418-13.6h6.196z"
        fill="#FCA326"
      />
      <path
        d="M22.614 9.45l1.342 4.135a.924.924 0 0 1-.331 1.023L12 23.054z"
        fill="#E24329"
      />
      <path
        d="M22.614 9.452h-6.196l2.664-8.189a.455.455 0 0 1 .867 0z"
        fill="#FC6D26"
      />
    </svg>
  );
}

function DiscordIcon({ className }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24">
      <path
        d="M20.317 4.3698a19.7913 19.7913 0 0 0-4.8851-1.5152.0741.0741 0 0 0-.0785.0371c-.211.3753-.4447.8648-.6083 1.2495-1.8447-.2762-3.68-.2762-5.4868 0-.1636-.3933-.4058-.8742-.6177-1.2495a.077.077 0 0 0-.0785-.037 19.7363 19.7363 0 0 0-4.8852 1.515.0699.0699 0 0 0-.0321.0277C.5334 9.0458-.319 13.5799.0992 18.0578a.0824.0824 0 0 0 .0312.0561c2.0528 1.5076 4.0413 2.4228 5.9929 3.0294a.0777.0777 0 0 0 .0842-.0276c.4616-.6304.8731-1.2952 1.226-1.9942a.076.076 0 0 0-.0416-.1057c-.6528-.2476-1.2743-.5495-1.8722-.8923a.077.077 0 0 1-.0076-.1277c.1258-.0943.2517-.1923.3718-.2914a.0743.0743 0 0 1 .0776-.0105c3.9278 1.7933 8.18 1.7933 12.0614 0a.0739.0739 0 0 1 .0785.0095c.1202.099.246.1981.3728.2924a.077.077 0 0 1-.0066.1276 12.2986 12.2986 0 0 1-1.873.8914.0766.0766 0 0 0-.0407.1067c.3604.6989.7719 1.3637 1.225 1.9942a.076.076 0 0 0 .0842.0286c1.961-.6067 3.9495-1.5219 6.0023-3.0294a.077.077 0 0 0 .0313-.0552c.5004-5.177-.8382-9.6739-3.5485-13.6604a.061.061 0 0 0-.0312-.0286zM8.02 15.3312c-1.1825 0-2.1569-1.0857-2.1569-2.419 0-1.3332.9555-2.4189 2.157-2.4189 1.2108 0 2.1757 1.0952 2.1568 2.419 0 1.3332-.9555 2.4189-2.1569 2.4189zm7.9748 0c-1.1825 0-2.1569-1.0857-2.1569-2.419 0-1.3332.9554-2.4189 2.1569-2.4189 1.2108 0 2.1757 1.0952 2.1568 2.419 0 1.3332-.946 2.4189-2.1568 2.4189Z"
        fill="#5865F2"
      />
    </svg>
  );
}

function OIDCIcon({ className }: { className?: string }) {
  // Generic shield glyph for the configurable OIDC/SSO connector — unlike the
  // other providers this isn't a fixed brand, so no brand mark applies.
  return (
    <svg
      className={className}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.75"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d="M12 2.5l7.5 3.2v5.2c0 5.1-3.2 8.9-7.5 10.6-4.3-1.7-7.5-5.5-7.5-10.6V5.7L12 2.5z" />
      <path d="M9 12l2 2 4-4" />
    </svg>
  );
}

function SAMLIcon({ className }: { className?: string }) {
  // Generic key glyph for the configurable SAML SP connector — same
  // rationale as OIDCIcon: this isn't a fixed brand, so a neutral mark
  // distinct from the OIDC shield keeps the two SSO mechanisms visually
  // distinguishable on the login page.
  return (
    <svg
      className={className}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.75"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <circle cx="8" cy="15" r="3.5" />
      <path d="M10.5 12.5L19 4M19 4h-3.5M19 4v3.5" />
    </svg>
  );
}

const PROVIDER_ICONS: Record<string, React.FC<{ className?: string }>> = {
  google: GoogleIcon,
  slack: SlackIcon,
  github: GitHubIcon,
  microsoft: MicrosoftIcon,
  gitlab: GitLabIcon,
  discord: DiscordIcon,
  oidc: OIDCIcon,
  saml: SAMLIcon,
};

function LoginPage() {
  const { t } = useTranslation("auth");
  const { t: tc } = useTranslation("common");
  const navigate = useNavigate();
  const { org } = Route.useParams();
  const { session_expired, returnTo, demo: demoAutoLogin } = useSearch({
    from: "/orgs/$org/login",
  });
  const auth = useAuth();
  const {
    login,
    logout,
    switchOrg,
    isAuthenticated,
    verify2FA,
    applyLoginResponse,
  } = auth;
  // Rename to skip the react-hooks/rules-of-hooks linter — auth.useRecoveryCode
  // is a service method, not a React hook, but its name triggers the rule.
  const submitRecoveryCode = auth.useRecoveryCode;
  const { data: versionData } = useVersion();
  const { data: providersData } = useProviders();
  const providers = providersData?.providers;
  const registrationEnabled = providersData?.registrationEnabled;

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [availableOrgs, setAvailableOrgs] = useState<OrganizationSummary[]>([]);
  const [showOrgPicker, setShowOrgPicker] = useState(false);
  const [twoFAState, setTwoFAState] = useState<{ tempToken: string } | null>(null);
  const [twoFACode, setTwoFACode] = useState("");
  const [showRecovery, setShowRecovery] = useState(false);
  const [passkeysEnabled, setPasskeysEnabled] = useState(false);
  // The method this browser used last (read once on mount). Drives which
  // option is promoted to the top of the card with a "Last used" badge.
  const [lastAuthMethod] = useState<string | null>(() => getLastAuthMethod());

  // Read passkey support from /auth/providers and probe browser
  // capability — both are needed before we render the passkey button or
  // arm the conditional UI hook.
  useEffect(() => {
    let cancelled = false;
    getAuthProviders()
      .then((p) => {
        if (!cancelled) setPasskeysEnabled(p.passkeysEnabled);
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, []);

  // Send a resolved login destination on its way. An in-app `returnTo`
  // (`{ href }`) needs a full navigation because it's an arbitrary path
  // outside this route's param shape; the org-root fallback (`{ to, params }`)
  // is a plain SPA navigate. `replace` keeps /login out of history.
  //
  // MCP OAuth consent bounce: when the destination is the embedded OAuth
  // /authorize endpoint, force a token refresh first. That endpoint (and the
  // consent screen's native form POST after it) authenticates via the
  // `access_token` COOKIE, not the SPA's localStorage bearer — and the two
  // routinely diverge: SSO logins hand tokens over in the redirect URL and
  // never set the cookie, and an idle tab's cookie lapses while the bearer
  // session keeps refreshing. POST /auth/refresh re-sets the cookie
  // (server-side, alongside the rotated bearer), so refreshing right before
  // the full-page navigation guarantees /authorize sees a session instead of
  // bouncing straight back here in a loop. A refresh failure falls through to
  // the navigation anyway — a definitively-dead session has already been
  // cleared and redirected by token-refresh's escalation.
  const goToDestination = useCallback(
    (dest: LoginDestination, replace = false) => {
      if ("href" in dest) {
        const go = () => {
          if (replace) window.location.replace(dest.href);
          else window.location.href = dest.href;
        };
        if (isOAuthAuthorizeReturnTo(dest.href)) {
          void refreshAccessToken()
            .catch(() => null)
            .then(go);
        } else {
          go();
        }
      } else {
        navigate({ to: dest.to, params: dest.params, replace });
      }
    },
    [navigate],
  );

  // Redirect if already authenticated (but not when showing org picker). When
  // a valid `returnTo` deep link is present, honor it instead of the org root
  // — this also matches routeResult's default case, so the two paths racing on
  // the same isAuthenticated flip now agree on the destination.
  useEffect(() => {
    if (isAuthenticated && !showOrgPicker) {
      goToDestination(resolveDestination(org, returnTo, BASE_PATH), true);
    }
  }, [isAuthenticated, showOrgPicker, org, returnTo, goToDestination]);

  const routeResult = useCallback(
    (result: LoginResult) => {
      if (result.requires2FA && result.tempToken) {
        setTwoFAState({ tempToken: result.tempToken });
        return;
      }

      switch (result.loginAction) {
        case "noOrg":
          navigate({ to: "/no-org" });
          break;
        case "orgChoice":
          setAvailableOrgs(result.organizations);
          setShowOrgPicker(true);
          if (result.resolvedOrg && result.resolvedOrg !== org) {
            navigate({
              to: "/orgs/$org/login",
              params: { org: result.resolvedOrg },
              search: { session_expired: false, returnTo },
              replace: true,
            });
          }
          break;
        case "orgRedirect":
          if (result.resolvedOrg) {
            goToDestination(
              resolveDestination(result.resolvedOrg, returnTo, BASE_PATH),
            );
          }
          break;
        default:
          goToDestination(
            resolveDestination(result.resolvedOrg || org, returnTo, BASE_PATH),
          );
          break;
      }
    },
    [navigate, org, returnTo, goToDestination],
  );

  const reportError = useCallback(
    (err: unknown) => {
      if (err instanceof ApiError) {
        setError(err.message);
      } else {
        setError(tc("unexpectedError"));
      }
    },
    [tc],
  );

  // The shared public live demo (spec 2026-09-06-02). Nothing is rendered when
  // the instance has no demo, so a self-hosted install shows exactly what it
  // showed before.
  const demoConfig = useDemoConfig();
  const demoAvailable = Boolean(
    demoConfig.enabled && demoConfig.orgSlug && demoConfig.email && demoConfig.password,
  );

  // Signing into the demo goes through the ORDINARY login — the same
  // login(org, email, password) the form calls, and the same routeResult
  // afterwards. There is deliberately no session-minting shortcut: the demo is
  // a real account, and giving it a bespoke entry point would be a second
  // authentication path to keep correct forever.
  const enterDemo = useCallback(async () => {
    if (!demoAvailable) return;

    setError(null);
    setIsLoading(true);

    try {
      const result = await login(
        demoConfig.orgSlug as string,
        demoConfig.email as string,
        demoConfig.password as string,
      );
      routeResult(result);
    } catch (err) {
      reportError(err);
    } finally {
      setIsLoading(false);
    }
  }, [demoAvailable, demoConfig, login, routeResult, reportError]);

  // `?demo=1` enters the demo on load. A REF, not state: the flag exists only
  // to make the effect fire once — the public-config query resolving is itself
  // a re-render, and without the latch that second pass would start a second
  // login while the first is still in flight. Writing state here would also
  // trip react-hooks' cascading-render rule for no benefit, since nothing
  // renders off this value.
  const demoAutoLoginStarted = useRef(false);

  useEffect(() => {
    if (!demoAutoLogin || !demoAvailable || demoAutoLoginStarted.current) return;

    demoAutoLoginStarted.current = true;
    void enterDemo();
  }, [demoAutoLogin, demoAvailable, enterDemo]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setIsLoading(true);

    try {
      const result = await login(org, email, password);
      // login() resolved without throwing — the password was correct (a 2FA
      // step may still follow). Record the choice as the last-used method.
      setLastAuthMethod("password");
      routeResult(result);
    } catch (err) {
      reportError(err);
    } finally {
      setIsLoading(false);
    }
  };

  const handle2FAVerify = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!twoFAState) return;
    setError(null);
    setIsLoading(true);
    try {
      const result = showRecovery
        ? await submitRecoveryCode(twoFAState.tempToken, twoFACode)
        : await verify2FA(twoFAState.tempToken, twoFACode);
      setTwoFACode("");
      setTwoFAState(null);
      routeResult(result);
    } catch (err) {
      reportError(err);
    } finally {
      setIsLoading(false);
    }
  };

  const handlePasskeyLogin = async () => {
    setError(null);
    setIsLoading(true);
    try {
      const begin = await beginPasskeyLogin(email || undefined);
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const optionsJSON = (begin.options as any).publicKey ?? begin.options;
      const credential = await startAuthentication({ optionsJSON });
      const data = await finishPasskeyLogin(begin.session, credential, org);
      const result = await applyLoginResponse(data as never);
      // Successful ceremony — record passkey as the last-used method.
      setLastAuthMethod("passkey");
      routeResult(result);
    } catch (err) {
      // Surface a precise, passkey-specific message instead of the generic
      // "unexpected error" banner. User-cancel stays silent.
      switch (classifyPasskeyError(err)) {
        case "cancelled":
          return; // silent
        case "domainMismatch":
          setError(t("passkeyDomainMismatch"));
          return;
        case "failed":
          setError(t("passkeyFailed"));
          return;
        default:
          reportError(err); // ApiError / generic
      }
    } finally {
      setIsLoading(false);
    }
  };

  // Conditional UI: when passkeys are enabled and the browser supports
  // autofill, fire a discoverable login ceremony in the background. The
  // browser surfaces available passkeys in the email field's autofill
  // chip; selecting one completes the sign-in. NotAllowedError fires
  // when the user types instead of picking a passkey — quietly ignore
  // it so we don't spam the console.
  useEffect(() => {
    if (!passkeysEnabled) return;
    if (!browserSupportsWebAuthn() || !browserSupportsWebAuthnAutofill()) return;
    let cancelled = false;
    (async () => {
      try {
        const begin = await beginPasskeyLogin();
        if (cancelled) return;
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        const optionsJSON = (begin.options as any).publicKey ?? begin.options;
        const credential = await startAuthentication({
          optionsJSON,
          useBrowserAutofill: true,
        });
        if (cancelled) return;
        const data = await finishPasskeyLogin(begin.session, credential, org);
        const result = await applyLoginResponse(data as never);
        // Autofill ceremony succeeded — record passkey as the last-used method.
        setLastAuthMethod("passkey");
        routeResult(result);
      } catch (err) {
        if (cancelled) return;
        const kind = classifyPasskeyError(err);
        // Conditional UI is best-effort; log only in dev console. Stay quiet on
        // user-cancel and on a domain mismatch (a misconfigured RP ID would
        // otherwise spam the console on every page load).
        if (kind !== "cancelled" && kind !== "domainMismatch") {
          console.warn("conditional UI failed", err);
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [passkeysEnabled, applyLoginResponse, org, routeResult]);

  if (isAuthenticated && !showOrgPicker) {
    return null;
  }

  const handleOrgSelect = async (orgSlug: string) => {
    setIsLoading(true);
    try {
      if (orgSlug !== org) {
        await switchOrg(orgSlug);
      }
      // Honor the deep link only when it targets the org just picked; picking
      // a different org falls back to that org's root (see the spec's
      // org-mismatch rule / Open-questions default).
      goToDestination(resolveDestination(orgSlug, returnTo, BASE_PATH));
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message);
      } else {
        setError(tc("unexpectedError"));
      }
    } finally {
      setIsLoading(false);
    }
  };

  const handleUseAnotherAccount = async () => {
    await logout();
    setShowOrgPicker(false);
    setAvailableOrgs([]);
    setError(null);
  };

  const handleOAuthLogin = (providerType: string) => {
    // OAuth redirects away from the app, so we can't observe success here —
    // record the intent immediately before the redirect.
    setLastAuthMethod(`oauth:${providerType}`);
    // MCP OAuth consent bounce: the provider callback appends the session
    // tokens to redirect_uri as query params, which only the SPA's pre-React
    // handoff (main.tsx) knows how to persist. Sending the callback straight
    // to /api/v1/oauth/authorize would drop those tokens on a non-SPA URL —
    // so land back on THIS login page instead, with returnTo preserved; the
    // handoff stores the session and the already-authenticated effect then
    // resumes the authorize flow (with the cookie refreshed by
    // goToDestination).
    // A previous failed attempt left `error`/`error_description` on this URL,
    // and the 401 bounce captured them into `returnTo`. Strip them before the
    // value becomes a redirect_uri, or each retry nests the last failure one
    // URL-encoding deeper (spec 2026-08-25-01).
    const currentPath = isOAuthAuthorizeReturnTo(returnTo)
      ? `${BASE_PATH}/orgs/${org}/login?returnTo=${encodeURIComponent(returnTo)}`
      : stripOAuthErrorParams(returnTo || `/dash0/orgs/${org}`);
    const loginUrl = `/api/v1/auth/${providerType}/login?org=${encodeURIComponent(org)}&redirect_uri=${encodeURIComponent(currentPath)}`;
    window.location.href = loginUrl;
  };

  // Resolve the promoted (last-used) method, but only when it is still
  // available — never promote a provider that was removed or a passkey when
  // passkeys are disabled / unsupported.
  const passkeySupported = passkeysEnabled && browserSupportsWebAuthn();
  const promotedProvider =
    lastAuthMethod && lastAuthMethod.startsWith("oauth:")
      ? providers?.find(
          (p) => p.type === lastAuthMethod.slice("oauth:".length),
        ) ?? null
      : null;
  const promotePasskey = lastAuthMethod === "passkey" && passkeySupported;
  const promotePassword = lastAuthMethod === "password";
  // Providers shown in the grid below, with the promoted one removed so it
  // isn't listed twice.
  const gridProviders = promotedProvider
    ? providers?.filter((p) => p.type !== promotedProvider.type)
    : providers;
  const hasGridProviders = gridProviders && gridProviders.length > 0;

  return (
    <AuthSplitLayout>
      <Card className="w-full max-w-md border-t-4 border-t-brand">
        <CardHeader className="text-center">
          <div className="flex justify-center mb-4" data-testid="login-logo">
            <Logo size={64} />
          </div>
          <CardTitle className="text-2xl" data-testid="login-title">
            SolidPing
          </CardTitle>
          <p className="text-sm text-muted-foreground mt-1">
            {t("organizationLabel", { org })}
          </p>
        </CardHeader>
        <CardContent>
          {session_expired && (
            <Alert className="mb-4">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>
                {t("sessionExpired")}
              </AlertDescription>
            </Alert>
          )}

          {error && (
            <Alert
              variant="destructive"
              className="mb-4"
              data-testid="login-error"
            >
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}

          {twoFAState ? (
            <form onSubmit={handle2FAVerify} className="space-y-4">
              <p className="text-sm text-muted-foreground text-center">
                {showRecovery ? t("twoFactor.recoveryPrompt") : t("twoFactor.codePrompt")}
              </p>
              <div className="space-y-2">
                <Label htmlFor={showRecovery ? "2fa-login-recovery-code" : "2fa-login-code"}>
                  {showRecovery ? t("twoFactor.recoveryLabel") : t("twoFactor.codeLabel")}
                </Label>
                <Input
                  id={showRecovery ? "2fa-login-recovery-code" : "2fa-login-code"}
                  data-testid={showRecovery ? "2fa-login-recovery-code" : "2fa-login-code"}
                  inputMode={showRecovery ? "text" : "numeric"}
                  maxLength={showRecovery ? 32 : 6}
                  value={twoFACode}
                  onChange={(e) =>
                    setTwoFACode(
                      showRecovery ? e.target.value : e.target.value.replace(/\D/g, ""),
                    )
                  }
                  required
                  autoFocus
                  className="font-mono"
                />
              </div>
              <Button
                type="submit"
                className="w-full"
                disabled={isLoading || (!showRecovery && twoFACode.length !== 6)}
                data-testid="2fa-login-verify"
              >
                {isLoading ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : null}
                {t("twoFactor.verify")}
              </Button>
              <div className="flex justify-between text-sm">
                <button
                  type="button"
                  className="text-muted-foreground hover:underline"
                  data-testid="2fa-login-back"
                  onClick={() => {
                    setTwoFAState(null);
                    setTwoFACode("");
                    setShowRecovery(false);
                  }}
                >
                  {t("twoFactor.back")}
                </button>
                <button
                  type="button"
                  className="text-muted-foreground hover:underline"
                  data-testid="2fa-login-recovery-link"
                  onClick={() => {
                    setShowRecovery((v) => !v);
                    setTwoFACode("");
                  }}
                >
                  {showRecovery ? t("twoFactor.useCode") : t("twoFactor.useRecovery")}
                </button>
              </div>
            </form>
          ) : showOrgPicker ? (
            <div className="space-y-3" data-testid="org-picker">
              <p className="text-sm text-muted-foreground text-center">
                {t("selectOrganization")}
              </p>
              <div className="space-y-2">
                {availableOrgs.map((availOrg) => (
                  <Button
                    key={availOrg.slug}
                    variant="outline"
                    className="w-full justify-start"
                    disabled={isLoading}
                    onClick={() => handleOrgSelect(availOrg.slug)}
                    data-testid={`org-picker-${availOrg.slug}`}
                  >
                    <Building2 className="mr-2 h-4 w-4" />
                    {availOrg.name || availOrg.slug}
                    <span className="ml-auto text-xs text-muted-foreground">
                      {availOrg.role}
                    </span>
                  </Button>
                ))}
              </div>
              <div className="pt-2 text-center">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={handleUseAnotherAccount}
                  data-testid="use-another-account"
                >
                  {t("useAnotherAccount")}
                </Button>
              </div>
            </div>
          ) : (
            <>
              {(promotedProvider || promotePasskey) && (
                <div className="mb-3" data-testid="login-last-used">
                  {promotedProvider ? (
                    (() => {
                      const Icon = PROVIDER_ICONS[promotedProvider.type];
                      return (
                        <Button
                          variant="outline"
                          className="w-full"
                          disabled={isLoading}
                          onClick={() => handleOAuthLogin(promotedProvider.type)}
                          data-testid={`login-oauth-${promotedProvider.type}-promoted`}
                        >
                          {Icon && <Icon className="mr-2 h-4 w-4" />}
                          {t("continueWith", { name: promotedProvider.name })}
                          <Badge
                            variant="secondary"
                            className="ml-2"
                            data-testid="login-last-used-badge"
                          >
                            {t("lastUsed")}
                          </Badge>
                        </Button>
                      );
                    })()
                  ) : (
                    <Button
                      type="button"
                      variant="outline"
                      className="w-full"
                      onClick={handlePasskeyLogin}
                      disabled={isLoading}
                      data-testid="passkey-login-button-promoted"
                    >
                      <KeyRound className="mr-2 h-4 w-4" />
                      {t("twoFactor.signInWithPasskey")}
                      <Badge
                        variant="secondary"
                        className="ml-2"
                        data-testid="login-last-used-badge"
                      >
                        {t("lastUsed")}
                      </Badge>
                    </Button>
                  )}
                  <div className="relative my-3">
                    <div className="absolute inset-0 flex items-center">
                      <span className="w-full border-t" />
                    </div>
                    <div className="relative flex justify-center text-xs uppercase">
                      <span className="bg-card px-2 text-muted-foreground">
                        {tc("or")}
                      </span>
                    </div>
                  </div>
                </div>
              )}

              {hasGridProviders && (
                <div className="mb-3">
                  <div className="grid grid-cols-2 gap-2">
                    {gridProviders.map((provider) => {
                      const Icon = PROVIDER_ICONS[provider.type];
                      return (
                        <Button
                          key={provider.type}
                          variant="outline"
                          size="sm"
                          className="w-full"
                          disabled={isLoading}
                          onClick={() => handleOAuthLogin(provider.type)}
                          data-testid={`login-oauth-${provider.type}`}
                        >
                          {Icon && <Icon className="mr-1.5 h-4 w-4" />}
                          {provider.name}
                        </Button>
                      );
                    })}
                  </div>
                  <div className="relative my-3">
                    <div className="absolute inset-0 flex items-center">
                      <span className="w-full border-t" />
                    </div>
                    <div className="relative flex justify-center text-xs uppercase">
                      <span className="bg-card px-2 text-muted-foreground">
                        {tc("or")}
                      </span>
                    </div>
                  </div>
                </div>
              )}

              <form onSubmit={handleSubmit} className="space-y-4">
                <div className="space-y-2">
                  <Label htmlFor="email">{tc("email")}</Label>
                  <Input
                    id="email"
                    type="email"
                    placeholder="test@test.com"
                    autoComplete={passkeysEnabled ? "username webauthn" : "username"}
                    value={email}
                    onChange={(e) => setEmail(e.target.value)}
                    required
                    disabled={isLoading}
                    // Autofocus when password was the last-used method so a
                    // returning user can start typing immediately.
                    autoFocus={promotePassword}
                    data-testid="login-email"
                  />
                </div>

                <div className="space-y-2">
                  <div className="flex items-center justify-between">
                    <Label htmlFor="password">{tc("password")}</Label>
                    <Link
                      to="/forgot-password"
                      search={{ email: email || undefined }}
                      reloadDocument
                      className="text-sm text-muted-foreground hover:underline"
                    >
                      {t("forgotPassword")}
                    </Link>
                  </div>
                  <PasswordInput
                    id="password"
                    autoComplete="current-password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    required
                    disabled={isLoading}
                    data-testid="login-password"
                  />
                </div>

                <Button
                  type="submit"
                  className="w-full"
                  disabled={isLoading}
                  data-testid="login-submit"
                >
                  {isLoading ? (
                    <>
                      <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                      {t("signingIn")}
                    </>
                  ) : (
                    <>
                      {t("signIn")}
                      {promotePassword && (
                        <Badge
                          variant="secondary"
                          className="ml-2"
                          data-testid="login-last-used-badge"
                        >
                          {t("lastUsed")}
                        </Badge>
                      )}
                    </>
                  )}
                </Button>
                {passkeysEnabled &&
                  browserSupportsWebAuthn() &&
                  !promotePasskey && (
                    <Button
                      type="button"
                      variant="outline"
                      className="w-full"
                      onClick={handlePasskeyLogin}
                      disabled={isLoading}
                      data-testid="passkey-login-button"
                    >
                      <KeyRound className="mr-2 h-4 w-4" />
                      {t("twoFactor.signInWithPasskey")}
                    </Button>
                  )}
              </form>

              {/* The live demo. Secondary to the real sign-in — an evaluator
                  wants it, a returning customer must not trip over it — and
                  rendered only when the instance actually offers one. */}
              {demoAvailable && (
                <div className="mt-4 border-t pt-4">
                  <Button
                    type="button"
                    variant="outline"
                    className="w-full"
                    onClick={() => void enterDemo()}
                    disabled={isLoading}
                    data-testid="login-demo"
                  >
                    <PlayCircle className="mr-2 h-4 w-4" />
                    {t("demo.tryLiveDemo")}
                  </Button>
                  <p className="mt-2 text-center text-xs text-muted-foreground">
                    {t("demo.loginHint")}
                  </p>
                </div>
              )}
            </>
          )}

          {registrationEnabled && (
            <div className="mt-4 text-center text-sm text-muted-foreground">
              {t("dontHaveAccount")}{" "}
              <Link
                to="/orgs/$org/register"
                params={{ org }}
                className="text-primary underline-offset-4 hover:underline"
              >
                {t("createOne")}
              </Link>
            </div>
          )}

          {versionData && (
            <div className="mt-6 pt-4 border-t text-center text-xs text-muted-foreground">
              <a
                href={marketingSiteUrl(versionData.deploymentMode)}
                target="_blank"
                rel="noopener noreferrer"
                data-testid="login-brand-link"
                className="underline-offset-4 hover:underline"
              >
                SolidPing
              </a>{" "}
              <a
                href={CHANGELOG_URL}
                target="_blank"
                rel="noopener noreferrer"
                data-testid="login-version"
                className="underline-offset-4 hover:underline"
              >
                v{versionData.version || "unknown"}
              </a>
              {(versionData.runMode === "demo" ||
                versionData.runMode === "test") && (
                <span
                  className="ml-2 px-2 py-0.5 rounded bg-yellow-100 text-yellow-800 dark:bg-yellow-900 dark:text-yellow-200"
                  data-testid="login-runmode"
                >
                  {versionData.runMode}
                </span>
              )}
            </div>
          )}
        </CardContent>
      </Card>
    </AuthSplitLayout>
  );
}
