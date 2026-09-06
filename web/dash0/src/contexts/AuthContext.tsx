import {
  createContext,
  useContext,
  useState,
  useEffect,
  useCallback,
  type ReactNode,
} from "react";
import {
  ApiError,
  apiFetch,
  setSession,
  clearToken,
  getToken,
  getRefreshToken,
  getExpiresAt,
  getExpiresInSeconds,
  redirectToPasswordChange,
} from "@/api/client";
import { refreshAccessToken, refreshWithOutcome, shouldRefreshNow } from "@/lib/token-refresh";
import { identifyAnalytics, resetAnalytics } from "@/lib/analytics";

interface User {
  // Pseudonymous user UUID. Used for the analytics distinct id; never shown.
  uid: string;
  email: string;
  name?: string;
  avatarUrl?: string;
  roles: string[];
  // isAdmin means "at least admin" — an owner outranks an admin and must pass
  // every admin gate (spec 2026-08-08-11).
  isAdmin: boolean;
  isOwner: boolean;
  isSuperAdmin: boolean;
  // isDemo marks the shared public live demo session (spec 2026-09-06-02).
  // Everything this session can see is shared with every other visitor, and
  // anything it creates is deleted within the hour — so the UI says so, and
  // hides the actions the server's write guard would refuse anyway.
  isDemo: boolean;
}

export interface OrganizationSummary {
  slug: string;
  name?: string;
  // Absolute http(s) URL, or a relative /pub/assets/<uid> path for an
  // uploaded logo. null when the org has none — render the default mark.
  logoUrl?: string | null;
  role: string;
}

export interface LoginResult {
  loginAction: string;
  organizations: OrganizationSummary[];
  resolvedOrg?: string;
  // 2FA challenge — when set, the login *did not* succeed; the caller
  // must collect a TOTP code (or recovery code) and call verify2FA /
  // useRecoveryCode with the temp token to complete the sign-in.
  requires2FA?: boolean;
  tempToken?: string;
}

// RenamedOrgSession is the session half of the PATCH /orgs/:org response.
export interface RenamedOrgSession {
  slug: string;
  accessToken: string;
  refreshToken?: string;
  expiresIn?: number;
}

// DeletedOrgSession is the login-shaped payload DELETE /orgs/:org answers with.
// Every field is optional on purpose: the org-less variant (the caller has no
// organization left) carries no `organization` and no `refreshToken` at all,
// and the server may answer an empty object when it could not mint anything.
export interface DeletedOrgSession {
  accessToken?: string;
  refreshToken?: string;
  expiresIn?: number;
  user?: {
    uid: string;
    email: string;
    name?: string;
    avatarUrl?: string;
    role: string;
    // See AuthResponse.user.demo.
    demo?: boolean;
  };
  organization?: { uid: string; slug: string; name?: string };
  organizations?: OrganizationSummary[];
  loginAction?: string;
}

interface AuthContextType {
  user: User | null;
  org: string | null;
  organizations: OrganizationSummary[];
  isAuthenticated: boolean;
  isLoading: boolean;
  login: (org: string, email: string, password: string) => Promise<LoginResult>;
  loginWithOAuth: (
    accessToken: string,
    orgSlug: string,
    refreshToken?: string,
    expiresIn?: number
  ) => Promise<void>;
  acceptInviteSession: (response: AuthResponse) => void;
  logout: () => Promise<void>;
  switchOrg: (orgSlug: string) => Promise<void>;
  // Adopts the session PATCH /orgs/:org hands back after an owner renames the
  // organization. The current access token is scoped to the OLD slug and would
  // 403 against the new one, so the tokens must be swapped in before the app
  // navigates to the new URL — this is switchOrg's problem without the
  // round-trip, since the server already minted the session.
  adoptRenamedOrgSession: (session: RenamedOrgSession) => Promise<void>;
  // Adopts the session DELETE /orgs/:org hands back after an owner deletes the
  // organization. The org the current token names no longer exists, so without
  // this the user would be logged out by their own maintenance action (issue
  // #206). Returns the slug the session landed on, or null for an org-less
  // session (the caller belongs to no organization any more) — the caller uses
  // it to decide between the next org's dashboard and /no-org.
  adoptOrgDeletionSession: (session: DeletedOrgSession) => string | null;
  refreshUser: () => Promise<void>;
  // Completes a 2FA login from a temp token + a TOTP / recovery code.
  // Both call paths return the same shape as a normal login result so
  // callers route on loginAction identically.
  verify2FA: (tempToken: string, code: string) => Promise<LoginResult>;
  useRecoveryCode: (tempToken: string, code: string) => Promise<LoginResult>;
  // Hands an already-issued passkey login response back to the auth
  // context so user/org/token state lands in the same place as a
  // password login.
  applyLoginResponse: (response: AuthResponse) => Promise<LoginResult>;
}

const AuthContext = createContext<AuthContextType | null>(null);

// Module-level (not React state) flag tracking whether a switchOrg() call is
// currently in flight. AuthProvider is mounted once app-wide, so a plain
// variable is enough — it doesn't need to be reactive, only readable at the
// moment OrgLayout's auto-switch-org guard effect (routes/orgs/$org.tsx)
// decides whether to fire its own switchOrg() call.
//
// Why this exists: a switcher UI (AppSidebar, CommandMenu, the organizations
// page) calls `await switchOrg(slug); navigate(...)`. switchOrg's internal
// `await apiFetch("/auth/me")` (line ~498) yields to the event loop *after*
// already updating `org` state — which lets OrgLayout, still mounted on the
// OLD url (navigate() hasn't run yet), re-render and see auth.org !== the
// still-old URL param. Its guard then fires ITS OWN switchOrg() call for the
// stale URL org, reverting the session mid-flight — a real, reproducible
// race (not a test flake): three switch-org requests ping-pong and the
// session can settle on the wrong org depending on which network call
// resolves last. Skipping the guard while an explicit switch is already in
// flight removes the conflicting call; the guard re-evaluates safely once
// the explicit switch finishes and the caller's navigate() catches the URL
// up to the (now-authoritative) org state.
let switchOrgInFlight = false;

/** Whether a switchOrg() call is currently in flight. See the flag's doc
 * comment above for why OrgLayout's auto-switch-org guard checks this. */
export function isSwitchOrgInFlight(): boolean {
  return switchOrgInFlight;
}

interface AuthResponse {
  accessToken: string;
  // Present on every login-shaped response (password, 2FA, passkey,
  // accept-invite, switch-org) — captured alongside the access token so the
  // client can silently refresh instead of hard-logging-out on expiry.
  refreshToken?: string;
  expiresIn?: number;
  user: {
    uid: string;
    email: string;
    name?: string;
    avatarUrl?: string;
    role: string;
    // Present (and true) only while the account must rotate its password.
    // The session handed over IS valid — it simply reaches nothing but the
    // rotation endpoint — so this is what routes the user to the
    // "set a new password" screen instead of letting them walk into a wall of
    // 403s on the dashboard's first data fetch.
    mustChangePassword?: boolean;
    // Present (and true) only for the shared public-demo account. It rides on
    // every login-shaped response and on /auth/me so the dashboard shows the
    // demo banner and hides what the write guard refuses PROACTIVELY, instead
    // of discovering the state by tripping a 403.
    demo?: boolean;
  };
  organization?: {
    uid: string;
    slug: string;
    name?: string;
  };
  organizations?: OrganizationSummary[];
  loginAction?: string;
  // The password-login path may return a 2FA challenge instead of
  // tokens. When requires2Fa is true, accessToken is empty and the
  // caller must complete the flow with verify2FA / useRecoveryCode.
  requires2Fa?: boolean;
  tempToken?: string;
}

interface MeResponse {
  user: {
    uid: string;
    email: string;
    name?: string;
    avatarUrl?: string;
    role: string;
    // See AuthResponse.user.mustChangePassword. /auth/me is deliberately one
    // of the three endpoints a flagged session can still reach, precisely so a
    // cold page load with a stored token can learn its own state here rather
    // than by failing somewhere less legible.
    mustChangePassword?: boolean;
  };
  // Optional — a zero-org session (a user who belongs to no organization yet)
  // gets a 200 from /auth/me with no organization. Mirrors the sibling
  // AuthResponse.organization above, which is already optional.
  organization?: {
    uid: string;
    slug: string;
    name?: string;
  };
  organizations: OrganizationSummary[];
}

const ORG_KEY = "solidping_org";

function getStoredOrg(): string | null {
  return localStorage.getItem(ORG_KEY);
}

function setStoredOrg(org: string): void {
  localStorage.setItem(ORG_KEY, org);
}

function clearStoredOrg(): void {
  localStorage.removeItem(ORG_KEY);
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [org, setOrg] = useState<string | null>(getStoredOrg());
  // Organization UUID, tracked alongside the slug purely so product analytics
  // can build the same pseudonymous distinct id as the backend. Never
  // persisted, never rendered.
  const [orgUid, setOrgUid] = useState<string | null>(null);
  const [organizations, setOrganizations] = useState<OrganizationSummary[]>([]);
  const [isLoading, setIsLoading] = useState(true);

  // Identify the analytics session whenever the resolved org/user pair
  // changes (login, session restore, org switch). Pseudonymous UUIDs only —
  // no email, no slug — and a pure no-op unless PostHog is configured.
  useEffect(() => {
    if (user?.uid) {
      identifyAnalytics(orgUid, user.uid);
    }
  }, [orgUid, user?.uid]);

  const validateSession = useCallback(async () => {
    const token = getToken();
    if (!token) {
      setIsLoading(false);
      return;
    }

    // Legacy/partial session: an access token with no expiry metadata (a
    // session predating expires_at tracking, or one of the funnel-audit
    // paths that used to drop it — see api/client.ts setSession) can't be
    // scheduled by the proactive timer (shouldRefreshNow needs both fields)
    // and won't reliably 401 soon on its own. Refresh once, up front, so it
    // either lands on solid footing (full session with a refresh token) or
    // is cleared immediately instead of coasting until a surprise 401 (spec
    // A.4).
    //
    // Only force this when there's actually a refresh token to spend. A
    // zero-org session (a user with no organization) has no refresh token by
    // design, so an unconditional up-front refresh would escalate
    // ("no-refresh-token" → clear + redirect) and log out a perfectly valid
    // session. With no refresh token we let /auth/me below be the arbiter: a
    // 200 (including one with no org) keeps the session, a 401/403 still
    // clears it in the catch. This defers the "give up" decision to real
    // evidence instead of blindly killing a session that /auth/me can still
    // validate — the genuinely-dead case is still cleared, just by /auth/me.
    if (getExpiresAt() === null && getRefreshToken() !== null) {
      const outcome = await refreshWithOutcome();
      if (!outcome.accessToken && outcome.failureReason !== "network-error") {
        // escalate() inside token-refresh.ts already cleared the session
        // and redirected to login — nothing left to validate.
        setIsLoading(false);
        return;
      }
    }

    try {
      const data = await apiFetch<MeResponse>(
        `/api/v1/auth/me`,
        { suppress401Redirect: true }
      );
      // A stored session restored on a cold page load never saw a login
      // response, so this is where such a tab discovers it is confined to the
      // rotation screen.
      if (data.user.mustChangePassword) {
        redirectToPasswordChange();
      }
      setUser({
        uid: data.user.uid,
        email: data.user.email,
        name: data.user.name,
        avatarUrl: data.user.avatarUrl,
        roles: [data.user.role],
        isAdmin:
          data.user.role === "owner" ||
          data.user.role === "admin" ||
          data.user.role === "superadmin",
        isOwner: data.user.role === "owner" || data.user.role === "superadmin",
        isSuperAdmin: data.user.role === "superadmin",
        isDemo: Boolean(data.user.demo),
      });
      // Update org from server response
      if (data.organization?.slug) {
        setStoredOrg(data.organization.slug);
        setOrg(data.organization.slug);
      }
      setOrgUid(data.organization?.uid ?? null);
      setOrganizations(data.organizations || []);
    } catch (e) {
      // Only clear auth state on auth-failure responses (401/403). Transient
      // errors — network aborts from navigation, 5xx, etc. — must not wipe a
      // freshly-issued OAuth token, or the next page load redirects to login.
      const isAuthFailure = e instanceof ApiError && (e.status === 401 || e.status === 403);
      if (isAuthFailure) {
        clearToken();
        clearStoredOrg();
        setUser(null);
        setOrg(null);
        setOrganizations([]);
      }
    } finally {
      setIsLoading(false);
    }
  }, []);

  useEffect(() => {
    validateSession();
  }, [validateSession]);

  // Proactive refresh: every 60s, check whether less than 1/3 of the
  // access-token lifetime remains; if so, refresh silently. This is what
  // keeps a merely-idle-in-the-background tab (no 401 to react to yet)
  // from ever reaching expiry in the first place — the reactive 401 path
  // in apiFetch is the fallback for when this timer didn't get there first
  // (tab was fully suspended, laptop asleep, etc.).
  useEffect(() => {
    const interval = setInterval(() => {
      if (shouldRefreshNow(getExpiresAt(), getExpiresInSeconds())) {
        void refreshAccessToken();
      }
    }, 60_000);

    return () => clearInterval(interval);
  }, []);

  const applyLoginResponse = async (data: AuthResponse): Promise<LoginResult> => {
    if (data.requires2Fa && data.tempToken) {
      return {
        loginAction: "",
        organizations: [],
        requires2FA: true,
        tempToken: data.tempToken,
      };
    }

    const loginAction = data.loginAction || "";
    const resolvedOrg = data.organization?.slug;

    setSession(data.accessToken, data.refreshToken, data.expiresIn);

    // Forced rotation: store the session (the rotation form needs it as its
    // proof of identity) and go straight to the screen. Every login-shaped
    // response funnels through here — password, 2FA, recovery code, passkey —
    // so no sign-in path can slip past it.
    if (data.user.mustChangePassword) {
      redirectToPasswordChange();
      return { loginAction: "", organizations: [], resolvedOrg };
    }

    if (resolvedOrg) {
      setStoredOrg(resolvedOrg);
      setOrg(resolvedOrg);
    }
    setOrgUid(data.organization?.uid ?? null);

    setUser({
      uid: data.user.uid,
      email: data.user.email,
      name: data.user.name,
      avatarUrl: data.user.avatarUrl,
      roles: [data.user.role],
      isAdmin:
        data.user.role === "owner" ||
        data.user.role === "admin" ||
        data.user.role === "superadmin",
      isOwner: data.user.role === "owner" || data.user.role === "superadmin",
      isSuperAdmin: data.user.role === "superadmin",
      isDemo: Boolean(data.user.demo),
    });

    const orgs = data.organizations || [];
    if (orgs.length > 0) {
      setOrganizations(orgs);
    } else {
      try {
        const meData = await apiFetch<MeResponse>(`/api/v1/auth/me`);
        setOrganizations(meData.organizations || []);
      } catch {
        setOrganizations([]);
      }
    }

    return { loginAction, organizations: orgs, resolvedOrg };
  };

  const login = async (orgSlug: string, email: string, password: string): Promise<LoginResult> => {
    const data = await apiFetch<AuthResponse>(
      `/api/v1/auth/login`,
      {
        method: "POST",
        body: JSON.stringify({ org: orgSlug, email, password }),
        skipAuth: true,
      }
    );

    return applyLoginResponse(data);
  };

  // The temp token from the 2FA login challenge travels as a Bearer header —
  // the backend never reads it from the body.
  const verify2FA = async (tempToken: string, code: string): Promise<LoginResult> => {
    const data = await apiFetch<AuthResponse>(`/api/v1/auth/2fa/verify`, {
      method: "POST",
      headers: { Authorization: `Bearer ${tempToken}` },
      body: JSON.stringify({ code }),
      skipAuth: true,
    });
    return applyLoginResponse(data);
  };

  const useRecoveryCode = async (tempToken: string, code: string): Promise<LoginResult> => {
    const data = await apiFetch<AuthResponse>(`/api/v1/auth/2fa/recovery`, {
      method: "POST",
      headers: { Authorization: `Bearer ${tempToken}` },
      body: JSON.stringify({ recoveryCode: code }),
      skipAuth: true,
    });
    return applyLoginResponse(data);
  };

  const acceptInviteSession = (data: AuthResponse) => {
    setSession(data.accessToken, data.refreshToken, data.expiresIn);
    if (data.organization?.slug) {
      setStoredOrg(data.organization.slug);
      setOrg(data.organization.slug);
    }
    setOrgUid(data.organization?.uid ?? null);
    setUser({
      uid: data.user.uid,
      email: data.user.email,
      name: data.user.name,
      avatarUrl: data.user.avatarUrl,
      roles: [data.user.role],
      isAdmin:
        data.user.role === "owner" ||
        data.user.role === "admin" ||
        data.user.role === "superadmin",
      isOwner: data.user.role === "owner" || data.user.role === "superadmin",
      isSuperAdmin: data.user.role === "superadmin",
      isDemo: Boolean(data.user.demo),
    });
    setOrganizations(data.organizations || []);
  };

  const loginWithOAuth = async (
    accessToken: string,
    orgSlug: string,
    refreshToken?: string,
    expiresIn?: number
  ) => {
    setSession(accessToken, refreshToken, expiresIn);
    setStoredOrg(orgSlug);
    setOrg(orgSlug);

    // Fetch user info using the token
    const data = await apiFetch<MeResponse>(
      `/api/v1/auth/me`
    );
    setOrgUid(data.organization?.uid ?? null);
    setUser({
      uid: data.user.uid,
      email: data.user.email,
      name: data.user.name,
      avatarUrl: data.user.avatarUrl,
      roles: [data.user.role],
      isAdmin:
        data.user.role === "owner" ||
        data.user.role === "admin" ||
        data.user.role === "superadmin",
      isOwner: data.user.role === "owner" || data.user.role === "superadmin",
      isSuperAdmin: data.user.role === "superadmin",
      isDemo: Boolean(data.user.demo),
    });
    setOrganizations(data.organizations || []);
  };

  const switchOrg = async (orgSlug: string) => {
    switchOrgInFlight = true;
    try {
      const data = await apiFetch<AuthResponse>(`/api/v1/auth/switch-org`, {
        method: "POST",
        body: JSON.stringify({ org: orgSlug }),
      });
      // switch-org mints a NEW refresh token scoped to the target org — the
      // client must overwrite (not merge/keep) the old one, or a subsequent
      // background refresh would silently flip back to the login-time org.
      setSession(data.accessToken, data.refreshToken, data.expiresIn);
      const resolvedOrg = data.organization?.slug || orgSlug;
      setStoredOrg(resolvedOrg);
      setOrg(resolvedOrg);
      setOrgUid(data.organization?.uid ?? null);
      setUser({
        uid: data.user.uid,
        email: data.user.email,
        name: data.user.name,
        avatarUrl: data.user.avatarUrl,
        roles: [data.user.role],
        isAdmin:
          data.user.role === "owner" ||
          data.user.role === "admin" ||
          data.user.role === "superadmin",
        isOwner: data.user.role === "owner" || data.user.role === "superadmin",
        isSuperAdmin: data.user.role === "superadmin",
        isDemo: Boolean(data.user.demo),
      });
      // Re-fetch organizations from /me (consistent with login)
      try {
        const meData = await apiFetch<MeResponse>(`/api/v1/auth/me`);
        setOrganizations(meData.organizations || []);
      } catch {
        // Fallback: preserve existing list with updated role
        setOrganizations((prev) =>
          prev.map((o) =>
            o.slug === resolvedOrg ? { ...o, role: data.user.role } : o
          )
        );
      }
    } finally {
      switchOrgInFlight = false;
    }
  };

  const adoptRenamedOrgSession = async (session: RenamedOrgSession) => {
    // Overwrite (never merge) both tokens: the refresh token was re-minted for
    // the renamed org, and keeping the old one would let a background refresh
    // resurrect a session scoped to a slug that no longer exists.
    setSession(session.accessToken, session.refreshToken, session.expiresIn);
    setStoredOrg(session.slug);
    setOrg(session.slug);

    const data = await apiFetch<MeResponse>(`/api/v1/auth/me`);
    setOrgUid(data.organization?.uid ?? null);
    setOrganizations(data.organizations || []);
  };

  // Synchronous on purpose: the caller navigates away the moment this returns,
  // and an awaited /auth/me round-trip in between is exactly the window in
  // which a query addressed to the deleted org can escalate to "session
  // expired". Everything needed (user, org, switcher list) is already in the
  // DELETE response.
  const adoptOrgDeletionSession = (session: DeletedOrgSession): string | null => {
    const nextOrg = session.organization?.slug ?? null;

    if (session.accessToken) {
      // Drop the whole previous session first. The refresh token it holds was
      // scoped to the org that was just deleted and is revoked server-side, and
      // setSession does NOT overwrite a stored refresh token when the new
      // session has none (the org-less case) — leaving it in place would make
      // the next proactive refresh spend a dead token and log the user out.
      clearToken();
      setSession(session.accessToken, session.refreshToken, session.expiresIn);
    }

    if (nextOrg) {
      setStoredOrg(nextOrg);
      setOrg(nextOrg);
      setOrgUid(session.organization?.uid ?? null);
    } else {
      // Nothing may keep pointing at the deleted slug: both routes/login.tsx
      // and redirectToExpiredLogin fall back to the stored org.
      clearStoredOrg();
      setOrg(null);
      setOrgUid(null);
    }

    if (session.user) {
      setUser({
        uid: session.user.uid,
        email: session.user.email,
        name: session.user.name,
        avatarUrl: session.user.avatarUrl,
        roles: [session.user.role],
        isAdmin:
          session.user.role === "owner" ||
          session.user.role === "admin" ||
          session.user.role === "superadmin",
        isOwner:
          session.user.role === "owner" || session.user.role === "superadmin",
        isSuperAdmin: session.user.role === "superadmin",
        isDemo: Boolean(session.user.demo),
      });
    }

    // Always overwrite: the list the app was holding still contains the org
    // that just died.
    setOrganizations(session.organizations || []);

    return nextOrg;
  };

  const refreshUser = useCallback(async () => {
    const data = await apiFetch<MeResponse>(`/api/v1/auth/me`);
    setOrgUid(data.organization?.uid ?? null);
    setUser({
      uid: data.user.uid,
      email: data.user.email,
      name: data.user.name,
      avatarUrl: data.user.avatarUrl,
      roles: [data.user.role],
      isAdmin:
        data.user.role === "owner" ||
        data.user.role === "admin" ||
        data.user.role === "superadmin",
      isOwner: data.user.role === "owner" || data.user.role === "superadmin",
      isSuperAdmin: data.user.role === "superadmin",
      isDemo: Boolean(data.user.demo),
    });
    setOrganizations(data.organizations || []);
  }, []);

  const logout = async () => {
    try {
      await apiFetch(`/api/v1/auth/logout`, {
        method: "POST",
      });
    } catch {
      // Ignore logout errors
    } finally {
      clearToken();
      clearStoredOrg();
      setUser(null);
      setOrg(null);
      setOrgUid(null);
      setOrganizations([]);
      // Drop the pseudonymous analytics identity so a subsequent login on the
      // same browser is not stitched onto the previous user. No-op when
      // analytics is off.
      resetAnalytics();
    }
  };

  return (
    <AuthContext.Provider
      value={{
        user,
        org,
        organizations,
        isAuthenticated: !!user,
        isLoading,
        login,
        loginWithOAuth,
        acceptInviteSession,
        logout,
        switchOrg,
        adoptRenamedOrgSession,
        adoptOrgDeletionSession,
        refreshUser,
        verify2FA,
        useRecoveryCode,
        applyLoginResponse,
      }}
    >
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth() {
  const context = useContext(AuthContext);
  if (!context) {
    throw new Error("useAuth must be used within an AuthProvider");
  }
  return context;
}
