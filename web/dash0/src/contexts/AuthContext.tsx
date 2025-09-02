import {
  createContext,
  useContext,
  useState,
  useEffect,
  useCallback,
  type ReactNode,
} from "react";
import { apiFetch, setToken, clearToken, getToken } from "@/api/client";

interface User {
  email: string;
  name?: string;
  avatarUrl?: string;
  roles: string[];
  isAdmin: boolean;
  isSuperAdmin: boolean;
}

export interface OrganizationSummary {
  slug: string;
  name?: string;
  role: string;
}

export interface LoginResult {
  loginAction: string;
  organizations: OrganizationSummary[];
  resolvedOrg?: string;
}

interface AuthContextType {
  user: User | null;
  org: string | null;
  organizations: OrganizationSummary[];
  isAuthenticated: boolean;
  isLoading: boolean;
  login: (org: string, email: string, password: string) => Promise<LoginResult>;
  loginWithOAuth: (accessToken: string, orgSlug: string) => Promise<void>;
  logout: () => Promise<void>;
  switchOrg: (orgSlug: string) => Promise<void>;
  refreshUser: () => Promise<void>;
}

const AuthContext = createContext<AuthContextType | null>(null);

interface AuthResponse {
  accessToken: string;
  user: {
    email: string;
    name?: string;
    avatarUrl?: string;
    role: string;
  };
  organization?: {
    uid: string;
    slug: string;
    name?: string;
  };
  organizations?: OrganizationSummary[];
  loginAction?: string;
}

interface MeResponse {
  user: {
    email: string;
    name?: string;
    avatarUrl?: string;
    role: string;
  };
  organization: {
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
  const [organizations, setOrganizations] = useState<OrganizationSummary[]>([]);
  const [isLoading, setIsLoading] = useState(true);

  const validateSession = useCallback(async () => {
    const token = getToken();
    if (!token) {
      setIsLoading(false);
      return;
    }

    try {
      const data = await apiFetch<MeResponse>(
        `/api/v1/auth/me`,
        { suppress401Redirect: true }
      );
      setUser({
        email: data.user.email,
        name: data.user.name,
        avatarUrl: data.user.avatarUrl,
        roles: [data.user.role],
        isAdmin: data.user.role === "admin" || data.user.role === "superadmin",
        isSuperAdmin: data.user.role === "superadmin",
      });
      // Update org from server response
      if (data.organization?.slug) {
        setStoredOrg(data.organization.slug);
        setOrg(data.organization.slug);
      }
      setOrganizations(data.organizations || []);
    } catch {
      clearToken();
      clearStoredOrg();
      setUser(null);
      setOrg(null);
      setOrganizations([]);
    } finally {
      setIsLoading(false);
    }
  }, []);

  useEffect(() => {
    validateSession();
  }, [validateSession]);

  const login = async (orgSlug: string, email: string, password: string): Promise<LoginResult> => {
    const data = await apiFetch<AuthResponse>(
      `/api/v1/auth/login`,
      {
        method: "POST",
        body: JSON.stringify({ org: orgSlug, email, password }),
        skipAuth: true,
      }
    );

    const loginAction = data.loginAction || "";
    const resolvedOrg = data.organization?.slug;

    setToken(data.accessToken);

    if (resolvedOrg) {
      setStoredOrg(resolvedOrg);
      setOrg(resolvedOrg);
    }

    setUser({
      email: data.user.email,
      name: data.user.name,
      avatarUrl: data.user.avatarUrl,
      roles: [data.user.role],
      isAdmin: data.user.role === "admin" || data.user.role === "superadmin",
      isSuperAdmin: data.user.role === "superadmin",
    });

    // Use organizations from login response if available
    const orgs = data.organizations || [];
    if (orgs.length > 0) {
      setOrganizations(orgs);
    } else {
      // Fallback to /me for backward compatibility
      try {
        const meData = await apiFetch<MeResponse>(`/api/v1/auth/me`);
        setOrganizations(meData.organizations || []);
      } catch {
        setOrganizations([]);
      }
    }

    return { loginAction, organizations: orgs, resolvedOrg };
  };

  const loginWithOAuth = async (accessToken: string, orgSlug: string) => {
    setToken(accessToken);
    setStoredOrg(orgSlug);
    setOrg(orgSlug);

    // Fetch user info using the token
    const data = await apiFetch<MeResponse>(
      `/api/v1/auth/me`
    );
    setUser({
      email: data.user.email,
      name: data.user.name,
      avatarUrl: data.user.avatarUrl,
      roles: [data.user.role],
      isAdmin: data.user.role === "admin" || data.user.role === "superadmin",
      isSuperAdmin: data.user.role === "superadmin",
    });
    setOrganizations(data.organizations || []);
  };

  const switchOrg = async (orgSlug: string) => {
    const data = await apiFetch<AuthResponse>(`/api/v1/auth/switch-org`, {
      method: "POST",
      body: JSON.stringify({ org: orgSlug }),
    });
    setToken(data.accessToken);
    const resolvedOrg = data.organization?.slug || orgSlug;
    setStoredOrg(resolvedOrg);
    setOrg(resolvedOrg);
    setUser({
      email: data.user.email,
      name: data.user.name,
      avatarUrl: data.user.avatarUrl,
      roles: [data.user.role],
      isAdmin: data.user.role === "admin" || data.user.role === "superadmin",
      isSuperAdmin: data.user.role === "superadmin",
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
  };

  const refreshUser = useCallback(async () => {
    const data = await apiFetch<MeResponse>(`/api/v1/auth/me`);
    setUser({
      email: data.user.email,
      name: data.user.name,
      avatarUrl: data.user.avatarUrl,
      roles: [data.user.role],
      isAdmin: data.user.role === "admin" || data.user.role === "superadmin",
      isSuperAdmin: data.user.role === "superadmin",
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
      setOrganizations([]);
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
        logout,
        switchOrg,
        refreshUser,
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
