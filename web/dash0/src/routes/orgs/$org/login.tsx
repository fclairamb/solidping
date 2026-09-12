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
import { useDemoConfig, usePublicConfigLoading } from "@/api/public-config";
import {
  demoAutoLoginOwnsRedirect,
  demoEntryDecision,
  parseDemoFlag,
} from "@/lib/demo";
import { pickAccessibleOrg } from "@/lib/accessible-org";
import {
  getLastAuthMethod,
  setLastAuthMethod,
} from "@/lib/last-auth-method";
import {
  startAuthentication,
  browserSupportsWebAuthn,
  browserSupportsWebAuthnAutofill,
} from "@simplewebauthn/browser";
import { beginPasskeyLogin, finishPasskeyLogin } from "@/api/passkeys";
import { classifyPasskeyError } from "@/lib/passkey-error";
import {
  isOAuthAuthorizeReturnTo,
  resolveDestination,
  startOAuthLogin,
  type LoginDestination,
} from "@/lib/login-destination";
import { OAuthProviderButtons } from "@/components/auth/oauth-provider-buttons";
import { ProviderIcon } from "@/components/auth/provider-icon";
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
    // dashboard rather than into a login form. The coercion lives in
    // lib/demo.ts because `/`, `/login` and the `/orgs/$org` layout now parse
    // the same flag on the way here (spec 2026-09-07-02) — see parseDemoFlag
    // for why four shapes are accepted.
    // Optional in the emitted type, deliberately: every existing
    // `navigate({ to: "/orgs/$org/login", search: … })` in the app predates
    // this param and must keep compiling without naming it.
    demo: parseDemoFlag(search.demo),
  }),
  component: LoginPage,
});

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
    isLoading: authLoading,
    user,
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
  // /auth/providers carries the passkey flag alongside the provider list, so
  // one query answers both. This used to be a second fetch of the very same
  // endpoint through getAuthProviders() (spec 2026-09-09-02).
  const passkeysEnabled = providersData?.passkeysEnabled ?? false;

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [availableOrgs, setAvailableOrgs] = useState<OrganizationSummary[]>([]);
  const [showOrgPicker, setShowOrgPicker] = useState(false);
  const [twoFAState, setTwoFAState] = useState<{ tempToken: string } | null>(null);
  const [twoFACode, setTwoFACode] = useState("");
  const [showRecovery, setShowRecovery] = useState(false);
  // The method this browser used last (read once on mount). Drives which
  // option is promoted to the top of the card with a "Last used" badge.
  const [lastAuthMethod] = useState<string | null>(() => getLastAuthMethod());

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

  // The shared public live demo (spec 2026-09-06-02). Nothing is rendered when
  // the instance has no demo, so a self-hosted install shows exactly what it
  // showed before. Declared up here, above the redirect effect, because that
  // effect's dependency array reads `demoAvailable` during render.
  const demoConfig = useDemoConfig();
  const demoAvailable = Boolean(
    demoConfig.enabled && demoConfig.orgSlug && demoConfig.email && demoConfig.password,
  );
  const demoConfigLoading = usePublicConfigLoading();

  // Redirect if already authenticated (but not when showing org picker). When
  // a valid `returnTo` deep link is present, honor it instead of the org root
  // — this also matches routeResult's default case, so the two paths racing on
  // the same isAuthenticated flip now agree on the destination.
  //
  // `?demo` is the third branch (spec 2026-09-07-02): the flag means "put me in
  // the demo", not "put me wherever my token points". This effect used to win
  // the race against the auto-login one below, so a visitor who already held a
  // session — their own org, or even the demo itself — followed a demo link and
  // landed on /orgs/<the URL's org> instead. Standing down here hands the
  // decision to the demo effect, which either re-enters or short-circuits.
  //
  // It stands down only when that effect will actually act, though. On a
  // self-hosted install with the demo OFF, the auto-login effect declines to
  // run (it requires demoAvailable), and an authenticated visitor following a
  // …/login?demo=true link would be left on the `return null` render guard
  // below — a blank page, forever. demoAutoLoginOwnsRedirect keeps the two in
  // agreement, including during the window where the public-config document
  // has not answered yet and "no demo" is not yet knowable.
  const demoOwnsRedirect = demoAutoLoginOwnsRedirect(
    demoAutoLogin,
    demoAvailable,
    demoConfigLoading,
  );

  // The destination is the org this session can actually USE, not the org the
  // URL happens to name (spec 2026-09-08-01 §B). The URL's org is only ever a
  // hint here: /orgs/<slug>/login is reachable from a marketing link, a
  // bookmark, an old email, or the "Try the live demo" button offered on EVERY
  // org's login page. Sending an authenticated visitor to an org they are not
  // a member of dead-ends them on "Permission Denied", whose only button links
  // back to that same org.
  //
  // This is also the second half of the demo race (spec 2026-09-07-02 fixed
  // only the ?demo-flag branch): the button's own `routeResult` navigates to
  // /orgs/demo while login() flips isAuthenticated, and whichever navigation
  // commits last wins. Now both branches resolve to the same org, so the race
  // has no wrong outcome left to reach.
  //
  // The dependency list carries the session fields the pick reads
  // (auth.org / auth.organizations / isSuperAdmin) so the effect re-evaluates
  // against the session applyLoginResponse just stored rather than a stale one.
  const sessionOrg = auth.org;
  const sessionOrganizations = auth.organizations;
  const isSuperAdmin = user?.isSuperAdmin === true;

  useEffect(() => {
    if (demoOwnsRedirect) return;
    // `authLoading` is the session's "still resolving" signal, and this effect
    // must not act on a half-built one. applyLoginResponse raises it across the
    // /auth/me it falls back to when a login payload carries no organization
    // list; without the gate, the render committed in that window has
    // isAuthenticated=true with an EMPTY list, pickAccessibleOrg reads it as
    // "no organization at all", and a perfectly ordinary member is flashed
    // through /no-org before routeResult corrects the URL. The org layout's
    // twin branch ($org.tsx) already gates on the same flag — same signal,
    // same treatment, both call sites.
    if (authLoading) return;
    if (isAuthenticated && !showOrgPicker) {
      const accessibleOrg = pickAccessibleOrg(org, {
        org: sessionOrg,
        organizations: sessionOrganizations,
        isSuperAdmin,
      });
      // No organization at all — /no-org is the screen that offers creating or
      // joining one, and is where every other org-less path already lands.
      if (accessibleOrg === null) {
        navigate({ to: "/no-org", replace: true });
        return;
      }
      // resolveDestination needs no change: it already drops a `returnTo`
      // whose org segment differs from the resolved org, so a stale
      // returnTo=/d/orgs/<url org>/… cannot drag the visitor back.
      goToDestination(resolveDestination(accessibleOrg, returnTo, BASE_PATH), true);
    }
  }, [
    demoOwnsRedirect,
    authLoading,
    isAuthenticated,
    showOrgPicker,
    org,
    returnTo,
    goToDestination,
    navigate,
    sessionOrg,
    sessionOrganizations,
    isSuperAdmin,
  ]);

  // `loginOrg` is the org the credentials were actually for. It defaults to the
  // org whose login page we are on, which is right for every ordinary sign-in —
  // you land on /orgs/<yours>/login and log into <yours>. The live-demo button
  // (spec 2026-09-06-02) is the first caller where the two differ: it is offered
  // on EVERY org's login page and signs you into `demo`. Without the override,
  // a response that carries no explicit resolution falls back to the URL's org
  // and drops the visitor into an organization they are not a member of.
  const routeResult = useCallback(
    (result: LoginResult, loginOrg: string = org) => {
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
          if (result.resolvedOrg && result.resolvedOrg !== loginOrg) {
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
            resolveDestination(result.resolvedOrg || loginOrg, returnTo, BASE_PATH),
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

  // Signing into the demo goes through the ORDINARY login — the same
  // login(org, email, password) the form calls, and the same routeResult
  // afterwards. There is deliberately no session-minting shortcut: the demo is
  // a real account, and giving it a bespoke entry point would be a second
  // authentication path to keep correct forever.
  const enterDemo = useCallback(async () => {
    if (!demoAvailable) return;

    // Already the demo principal — re-entering must not mint a second session
    // for nothing, and must land in the DEMO org rather than in the org whose
    // login page this happens to be (which a demo user is not a member of).
    // The short-circuit lives here rather than in the ?demo effect below so it
    // covers the "Try the live demo" button too, and so the effect body stays
    // free of the conditional setState react-hooks refuses there.
    if (isAuthenticated && user?.isDemo && demoConfig.orgSlug) {
      navigate({
        to: "/orgs/$org",
        params: { org: demoConfig.orgSlug },
        replace: true,
      });
      return;
    }

    // The one invariant (spec 2026-09-12-01 §A): the demo is only ever signed
    // into from the demo org's OWN login page. Standing on another org's page,
    // hop there first and let its `?demo` effect do the sign-in — signing in
    // here would apply the demo session while the URL still names this org,
    // and the cross-org navigation that follows makes the org layout warn
    // "You don't have access to <this org> — showing <demo> instead." on the
    // product's own front door. `replace`, so Back returns to wherever the
    // visitor came from rather than to the page that bounced them.
    const decision = demoEntryDecision(org, demoConfig.orgSlug);
    if (decision === "unavailable") return;
    if (decision === "hopTo") {
      navigate({
        to: "/orgs/$org/login",
        params: { org: demoConfig.orgSlug as string },
        search: { session_expired: false, returnTo: undefined, demo: true },
        replace: true,
      });
      return;
    }

    setError(null);
    setIsLoading(true);

    try {
      const result = await login(
        demoConfig.orgSlug as string,
        demoConfig.email as string,
        demoConfig.password as string,
      );
      // Pass the demo org explicitly: the response carries no resolvedOrg for
      // an ordinary single-org login, and the fallback would otherwise be the
      // org whose login page this is. resolveDestination already refuses a
      // returnTo whose org does not match, so a stale one cannot drag the
      // visitor back out of the demo.
      routeResult(result, demoConfig.orgSlug as string);
    } catch (err) {
      reportError(err);
    } finally {
      setIsLoading(false);
    }
  }, [
    demoAvailable,
    demoConfig,
    isAuthenticated,
    user?.isDemo,
    navigate,
    org,
    login,
    routeResult,
    reportError,
  ]);

  // `?demo=1` enters the demo on load. A REF, not state: the flag exists only
  // to make the effect fire once — the public-config query resolving is itself
  // a re-render, and without the latch that second pass would start a second
  // login while the first is still in flight. Writing state here would also
  // trip react-hooks' cascading-render rule for no benefit, since nothing
  // renders off this value.
  //
  // It holds the ORG it fired for rather than a bare boolean: since spec
  // 2026-09-12-01 the effect's first act on a foreign org's page is to hop to
  // `/orgs/<demo>/login?demo=true`, and the latch has to re-arm there. Keying
  // it by org does that whether or not the router remounts this component
  // across the param change — a detail of the router we should not depend on.
  const demoAutoLoginStartedFor = useRef<string | null>(null);

  useEffect(() => {
    // `authLoading` joins the one-shot guard rather than sitting in a second
    // early return: the stored session must finish validating before the
    // decision is made (`user` is null while it is in flight, so a visitor who
    // is ALREADY in the demo would be treated as a stranger and signed in
    // again), and folding it in here keeps the effect body a single
    // unconditional call, which is what react-hooks/set-state-in-effect wants.
    if (
      !demoAutoLogin ||
      !demoAvailable ||
      authLoading ||
      demoAutoLoginStartedFor.current === org
    ) {
      return;
    }

    // enterDemo decides between "already the demo, just go there" and a real
    // login. login() replaces the stored session and org outright
    // (applyLoginResponse), so a visitor holding a session in their own org
    // needs no sign-out step — and gets no confirmation dialog either: the
    // whole point of the link is zero clicks.
    demoAutoLoginStartedFor.current = org;
    void enterDemo();
  }, [demoAutoLogin, demoAvailable, authLoading, org, enterDemo]);

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
                    <Button
                      variant="outline"
                      className="w-full"
                      disabled={isLoading}
                      onClick={() =>
                        // Same helper the grid below uses, so exactly one
                        // place knows the redirect shape.
                        startOAuthLogin({
                          org,
                          providerType: promotedProvider.type,
                          returnTo,
                        })
                      }
                      data-testid={`login-oauth-${promotedProvider.type}-promoted`}
                    >
                      <ProviderIcon
                        type={promotedProvider.type}
                        className="mr-2 h-4 w-4"
                      />
                      {t("continueWith", { name: promotedProvider.name })}
                      <Badge
                        variant="secondary"
                        className="ml-2"
                        data-testid="login-last-used-badge"
                      >
                        {t("lastUsed")}
                      </Badge>
                    </Button>
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

              <OAuthProviderButtons
                org={org}
                providers={gridProviders}
                disabled={isLoading}
                returnTo={returnTo}
                testIdPrefix="login"
              />

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
