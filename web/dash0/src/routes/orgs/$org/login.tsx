import { useState, useEffect } from "react";
import {
  createFileRoute,
  Link,
  useNavigate,
  useSearch,
} from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { useAuth, type OrganizationSummary } from "@/contexts/AuthContext";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Activity, AlertCircle, Loader2, Building2 } from "lucide-react";
import { ApiError } from "@/api/client";
import { useVersion, useProviders } from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/login")({
  validateSearch: (search: Record<string, unknown>) => ({
    session_expired: search.session_expired === "true",
    returnTo: typeof search.returnTo === "string" ? search.returnTo : undefined,
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

const PROVIDER_ICONS: Record<string, React.FC<{ className?: string }>> = {
  google: GoogleIcon,
  slack: SlackIcon,
  github: GitHubIcon,
  microsoft: MicrosoftIcon,
  gitlab: GitLabIcon,
  discord: DiscordIcon,
};

function LoginPage() {
  const { t } = useTranslation("auth");
  const { t: tc } = useTranslation("common");
  const navigate = useNavigate();
  const { org } = Route.useParams();
  const { session_expired, returnTo } = useSearch({ from: "/orgs/$org/login" });
  const { login, logout, switchOrg, isAuthenticated } = useAuth();
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

  // Redirect if already authenticated (but not when showing org picker)
  useEffect(() => {
    if (isAuthenticated && !showOrgPicker) {
      navigate({ to: "/orgs/$org", params: { org }, replace: true });
    }
  }, [isAuthenticated, navigate, org, showOrgPicker]);

  if (isAuthenticated && !showOrgPicker) {
    return null;
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setIsLoading(true);

    try {
      const result = await login(org, email, password);

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
            navigate({ to: "/orgs/$org", params: { org: result.resolvedOrg } });
          }
          break;

        default:
          if (returnTo && returnTo.includes("/orgs/")) {
            window.location.href = returnTo;
          } else {
            navigate({ to: "/orgs/$org", params: { org: result.resolvedOrg || org } });
          }
          break;
      }
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

  const handleOrgSelect = async (orgSlug: string) => {
    setIsLoading(true);
    try {
      if (orgSlug !== org) {
        await switchOrg(orgSlug);
      }
      navigate({ to: "/orgs/$org", params: { org: orgSlug } });
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
    const currentPath = returnTo || `/dash0/orgs/${org}`;
    const loginUrl = `/api/v1/auth/${providerType}/login?org=${encodeURIComponent(org)}&redirect_uri=${encodeURIComponent(currentPath)}`;
    window.location.href = loginUrl;
  };

  const hasProviders = providers && providers.length > 0;

  return (
    <div className="min-h-screen flex items-center justify-center bg-background p-4">
      <Card className="w-full max-w-md">
        <CardHeader className="text-center">
          <div className="flex justify-center mb-4" data-testid="login-logo">
            <Activity className="h-12 w-12 text-primary" />
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

          {showOrgPicker ? (
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
              {hasProviders && (
                <div className="mb-4">
                  <div className="grid grid-cols-2 gap-2">
                    {providers.map((provider) => {
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
                  <div className="relative my-4">
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
                    value={email}
                    onChange={(e) => setEmail(e.target.value)}
                    required
                    disabled={isLoading}
                    data-testid="login-email"
                  />
                </div>

                <div className="space-y-2">
                  <Label htmlFor="password">{tc("password")}</Label>
                  <Input
                    id="password"
                    type="password"
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
                    t("signIn")
                  )}
                </Button>
                <div className="text-center">
                  <Link
                    to="/forgot-password"
                    className="text-sm text-muted-foreground hover:underline"
                  >
                    {t("forgotPassword")}
                  </Link>
                </div>
              </form>
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
              <span data-testid="login-version">
                v{versionData.version || "unknown"}
              </span>
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
    </div>
  );
}
