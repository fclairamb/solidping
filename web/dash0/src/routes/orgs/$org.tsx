import { useState, useEffect } from "react";
import {
  createFileRoute,
  Link,
  Outlet,
  redirect,
  useLocation,
  useMatches,
  useNavigate,
} from "@tanstack/react-router";
import {
  AlertTriangle,
  BadgeCheck,
  Bug,
  Building,
  Calendar,
  ChevronRight,
  Globe,
  LayoutDashboard,
  ListChecks,
  Server,
  User2,
} from "lucide-react";
import {
  SidebarProvider,
  SidebarInset,
  SidebarTrigger,
} from "@/components/ui/sidebar";
import { AppSidebar } from "@/components/layout/AppSidebar";
import { CommandMenu, CommandMenuTrigger } from "@/components/CommandMenu";
import { Separator } from "@/components/ui/separator";
import { useAuth } from "@/contexts/AuthContext";
import { useCheck, useFeatures, useIncident, useStatusPage } from "@/api/hooks";
import { FeedbackButton } from "@/components/feedback/FeedbackButton";
import { FeedbackDialog } from "@/components/feedback/FeedbackDialog";
import { useFeedback } from "@/components/feedback/useFeedback";
import { useTranslation } from "react-i18next";

function hasOAuthTokenInURL(): boolean {
  const params = new URLSearchParams(window.location.search);
  return params.has("access_token");
}

export const Route = createFileRoute("/orgs/$org")({
  beforeLoad: ({ context, params, location }) => {
    // Don't redirect if we're on a public page (login, register)
    if (location.pathname.endsWith("/login") || location.pathname.endsWith("/register")) {
      return { org: params.org, isLoginPage: true };
    }
    // Allow through if OAuth callback tokens are present in the URL
    if (hasOAuthTokenInURL()) {
      return { org: params.org, isLoginPage: false };
    }
    // Skip redirect while auth is still loading (e.g. validating token on page refresh).
    // OrgLayout handles the redirect once auth resolves.
    if (!context.auth?.isLoading && !context.auth?.isAuthenticated) {
      const basepath = import.meta.env.VITE_BASE_URL || "";
      const returnTo = basepath + location.pathname + (location.searchStr || "");
      throw redirect({
        to: "/orgs/$org/login",
        params: { org: params.org },
        search: { session_expired: false, returnTo },
      });
    }
    // Store org in context for child routes
    return { org: params.org, isLoginPage: false };
  },
  component: OrgLayout,
});

const linkClass = "text-sm text-muted-foreground hover:text-foreground transition-colors inline-flex items-center gap-1";
const activeClass = "text-sm font-medium inline-flex items-center gap-1";
const iconClass = "h-3.5 w-3.5";

function BreadcrumbSeparator() {
  return <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />;
}

function Breadcrumbs({ org }: { org: string }) {
  const { t } = useTranslation("nav");
  const matches = useMatches();
  const routeIds = new Set(matches.map((m) => m.routeId));
  const params = Object.assign({}, ...matches.map((m) => m.params)) as Record<string, string>;

  // Determine the active section
  const isDashboard = routeIds.has("/orgs/$org/");
  const isChecks = matches.some((m) => m.routeId.startsWith("/orgs/$org/checks"));
  const isIncidents = matches.some((m) => m.routeId.startsWith("/orgs/$org/incidents"));
  const isEvents = routeIds.has("/orgs/$org/events");
  const isStatusPages = matches.some((m) => m.routeId.startsWith("/orgs/$org/status-pages"));

  // Checks section
  const { data: check } = useCheck(org, params.checkUid ?? "");
  // Incidents section
  const { data: incident } = useIncident(org, params.incidentUid ?? "");
  // Status pages section
  const { data: statusPage } = useStatusPage(org, params.statusPageUid ?? "");

  if (isDashboard) {
    return (
      <span className={activeClass}>
        <LayoutDashboard className={iconClass} />
        {t("dashboard")}
      </span>
    );
  }

  if (isChecks) {
    const checkUid = params.checkUid;
    const isCheckEdit = routeIds.has("/orgs/$org/checks/$checkUid/edit");
    const isNewCheck = routeIds.has("/orgs/$org/checks/new");
    const checkName = check?.name || check?.slug || checkUid?.slice(0, 8);

    return (
      <>
        {checkUid || isNewCheck ? (
          <Link to="/orgs/$org/checks" params={{ org }} className={linkClass}><ListChecks className={iconClass} />{t("checks")}</Link>
        ) : (
          <span className={activeClass}><ListChecks className={iconClass} />{t("checks")}</span>
        )}
        {isNewCheck && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{t("new")}</span>
          </>
        )}
        {checkUid && (
          <>
            <BreadcrumbSeparator />
            {isCheckEdit ? (
              <Link to="/orgs/$org/checks/$checkUid" params={{ org, checkUid }} search={{ graphPeriod: undefined, graphFull: undefined }} className={linkClass}>
                {checkName}
              </Link>
            ) : (
              <span className={activeClass}>{checkName}</span>
            )}
          </>
        )}
        {isCheckEdit && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{t("edit")}</span>
          </>
        )}
      </>
    );
  }

  if (isIncidents) {
    const incidentUid = params.incidentUid;
    const incidentLabel = incident?.checkName || incidentUid?.slice(0, 8);

    return (
      <>
        {incidentUid ? (
          <Link to="/orgs/$org/incidents" params={{ org }} search={{ state: "all" as const }} className={linkClass}><AlertTriangle className={iconClass} />{t("incidents")}</Link>
        ) : (
          <span className={activeClass}><AlertTriangle className={iconClass} />{t("incidents")}</span>
        )}
        {incidentUid && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{incidentLabel}</span>
          </>
        )}
      </>
    );
  }

  if (isEvents) {
    return <span className={activeClass}><Calendar className={iconClass} />{t("events")}</span>;
  }

  // Account section
  const isAccount = matches.some((m) => m.routeId.startsWith("/orgs/$org/account"));
  if (isAccount) {
    const isProfile = routeIds.has("/orgs/$org/account/profile");
    const isTokens = routeIds.has("/orgs/$org/account/tokens");
    const subLabel = isProfile ? t("profile") : isTokens ? t("tokens") : null;
    return (
      <>
        {subLabel ? (
          <Link to="/orgs/$org/account/profile" params={{ org }} className={linkClass}><User2 className={iconClass} />{t("account", { ns: "common" })}</Link>
        ) : (
          <span className={activeClass}><User2 className={iconClass} />{t("account", { ns: "common" })}</span>
        )}
        {subLabel && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{subLabel}</span>
          </>
        )}
      </>
    );
  }

  // Organization section
  const isOrganization = matches.some((m) => m.routeId.startsWith("/orgs/$org/organization"));
  if (isOrganization) {
    const isMembers = routeIds.has("/orgs/$org/organization/members");
    const isInvitations = routeIds.has("/orgs/$org/organization/invitations");
    const isSettings = routeIds.has("/orgs/$org/organization/settings");
    const subLabel = isMembers
      ? t("members")
      : isInvitations
        ? t("invitations")
        : isSettings
          ? t("settings")
          : null;
    return (
      <>
        {subLabel ? (
          <Link to="/orgs/$org/organization/members" params={{ org }} className={linkClass}><Building className={iconClass} />{t("organization", { ns: "common" })}</Link>
        ) : (
          <span className={activeClass}><Building className={iconClass} />{t("organization", { ns: "common" })}</span>
        )}
        {subLabel && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{subLabel}</span>
          </>
        )}
      </>
    );
  }

  // Server section
  const isServer = matches.some((m) => m.routeId.startsWith("/orgs/$org/server"));
  if (isServer) {
    const isWeb = routeIds.has("/orgs/$org/server/web");
    const isMail = routeIds.has("/orgs/$org/server/mail");
    const isAuth = routeIds.has("/orgs/$org/server/auth");
    const isPerformance = routeIds.has("/orgs/$org/server/performance");
    const subLabel = isWeb ? t("web") : isMail ? t("mail") : isAuth ? t("authentication") : isPerformance ? t("performance") : null;
    return (
      <>
        {subLabel ? (
          <Link to="/orgs/$org/server/web" params={{ org }} className={linkClass}><Server className={iconClass} />{t("server", { ns: "common" })}</Link>
        ) : (
          <span className={activeClass}><Server className={iconClass} />{t("server", { ns: "common" })}</span>
        )}
        {subLabel && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{subLabel}</span>
          </>
        )}
      </>
    );
  }

  if (isStatusPages) {
    const statusPageUid = params.statusPageUid;
    const isNew = routeIds.has("/orgs/$org/status-pages/new");
    const pageName = statusPage?.name || statusPageUid?.slice(0, 8);

    return (
      <>
        {statusPageUid || isNew ? (
          <Link to="/orgs/$org/status-pages" params={{ org }} className={linkClass}><Globe className={iconClass} />{t("statusPages")}</Link>
        ) : (
          <span className={activeClass}><Globe className={iconClass} />{t("statusPages")}</span>
        )}
        {isNew && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{t("new")}</span>
          </>
        )}
        {statusPageUid && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{pageName}</span>
          </>
        )}
      </>
    );
  }

  const isBadges = routeIds.has("/orgs/$org/badges");
  if (isBadges) {
    return (
      <span className={activeClass}>
        <BadgeCheck className={iconClass} />
        {t("badges")}
      </span>
    );
  }

  const isTest = matches.some((m) => m.routeId.startsWith("/orgs/$org/test"));
  if (isTest) {
    return (
      <span className={activeClass}>
        <Bug className={iconClass} />
        {t("testTools")}
      </span>
    );
  }

  return null;
}

function OrgLayout() {
  const { org } = Route.useParams();
  const location = useLocation();
  const navigate = useNavigate();
  const auth = useAuth();
  const isLoginPage = location.pathname.endsWith("/login") || location.pathname.endsWith("/register");
  const [oauthProcessing, setOauthProcessing] = useState(false);
  const [commandMenuOpen, setCommandMenuOpen] = useState(false);
  const { data: features } = useFeatures();
  const feedback = useFeedback({ enabled: features?.bugReport === true, org });

  // Handle OAuth callback tokens in URL
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const accessToken = params.get("access_token");
    const oauthOrg = params.get("org") || org;

    if (!accessToken) return;

    setOauthProcessing(true);
    auth
      .loginWithOAuth(accessToken, oauthOrg)
      .then(() => {
        // Hard navigation: forces a clean reload so URL/org context is in sync
        // before any child routes fire org-scoped API calls.
        const basepath = import.meta.env.VITE_BASE_URL || "";
        window.location.replace(`${basepath}/orgs/${oauthOrg}`);
      })
      .catch(() => {
        const basepath = import.meta.env.VITE_BASE_URL || "";
        window.location.replace(`${basepath}/orgs/${oauthOrg}/login?session_expired=false`);
      });
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  // Login page should render without sidebar
  if (isLoginPage) {
    return <Outlet />;
  }

  // Show nothing while processing OAuth callback
  if (oauthProcessing || hasOAuthTokenInURL()) {
    return null;
  }

  // Redirect to login once auth finishes loading and user is not authenticated.
  // This covers the case where beforeLoad allowed through while auth was still loading.
  if (!auth.isLoading && !auth.isAuthenticated) {
    const basepath = import.meta.env.VITE_BASE_URL || "";
    const returnTo = basepath + location.pathname + (location.searchStr || "");
    navigate({
      to: "/orgs/$org/login",
      params: { org },
      search: { session_expired: false, returnTo },
      replace: true,
    });
    return null;
  }

  return (
    <SidebarProvider defaultOpen={true}>
      <AppSidebar />
      <CommandMenu open={commandMenuOpen} onOpenChange={setCommandMenuOpen} />
      <SidebarInset className="md:ml-0">
        <header className="flex h-16 shrink-0 items-center gap-2 border-b px-4">
          <SidebarTrigger className="-ml-1" data-testid="sidebar-trigger" />
          <Separator orientation="vertical" className="mr-2 h-4" />
          <Breadcrumbs org={org} />
          <div className="ml-auto flex items-center gap-1">
            {features?.bugReport && (
              <FeedbackButton onClick={() => void feedback.open()} isCapturing={feedback.isCapturing} />
            )}
            <CommandMenuTrigger onOpen={() => setCommandMenuOpen(true)} />
          </div>
        </header>
        {features?.bugReport && (
          <FeedbackDialog
            open={feedback.isOpen}
            onOpenChange={(next) => (next ? null : feedback.close())}
            screenshot={feedback.screenshot}
            isCapturing={feedback.isCapturing}
            onSubmit={feedback.submit}
          />
        )}
        <div className="flex-1 overflow-auto p-3 sm:p-4">
          <Outlet />
        </div>
      </SidebarInset>
    </SidebarProvider>
  );
}
