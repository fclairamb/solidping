import { useState, useEffect, useRef } from "react";
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
  ArrowUpRight,
  BadgeCheck,
  Bell,
  BellRing,
  Loader2,
  Bug,
  Building,
  Calendar,
  CalendarClock,
  ChevronRight,
  GitBranch,
  Globe,
  LayoutDashboard,
  ListChecks,
  Megaphone,
  Network,
  Palette,
  Server,
  Target,
  User2,
  Workflow,
  Wrench,
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
import {
  useBackgroundJob,
  useCheck,
  useCheckGroup,
  useCheckJob,
  useIntegration,
  useEscalationPolicy,
  useFeatures,
  useIncident,
  useMaintenanceWindow,
  useOnCallSchedule,
  useOrgNotification,
  useReportSchedule,
  useResult,
  useSlo,
  useStatusPage,
  useStatusUpdate,
} from "@/api/hooks";
import { FeedbackButton } from "@/components/feedback/FeedbackButton";
import { FeedbackDialog } from "@/components/feedback/FeedbackDialog";
import { useFeedback } from "@/components/feedback/useFeedback";
import { LiveEventsProvider } from "@/contexts/LiveEventsContext";
import { useTranslation } from "react-i18next";

/** Parses the `?from=` search param used by the notification detail route. */
function parseNotificationFrom(
  from: string | undefined,
): { type: "incident" | "integration"; uid: string } | null {
  if (!from) return null;
  const colonIdx = from.indexOf(":");
  if (colonIdx === -1) return null;
  const type = from.slice(0, colonIdx);
  const uid = from.slice(colonIdx + 1);
  if (!uid) return null;
  if (type === "incident" || type === "integration") {
    return { type, uid };
  }
  return null;
}

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

// Route-id -> nav i18n key for every tab (and URL-only route) under
// /orgs/$org/organization. A flat table instead of a ternary chain so a new
// tab can't silently regress the breadcrumb again (see spec
// 2026-08-21-03-organization-breadcrumb-missing-tabs.md).
const ORG_SECTION_LABELS: Record<string, string> = {
  "/orgs/$org/organization/members": "members",
  "/orgs/$org/organization/invitations": "invitations",
  "/orgs/$org/organization/requests": "requests",
  "/orgs/$org/organization/usage": "usage",
  "/orgs/$org/organization/private-locations": "privateLocations",
  "/orgs/$org/organization/report-schedules": "reportSchedules",
  "/orgs/$org/organization/ai": "ai",
  "/orgs/$org/organization/settings": "settings",
};

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
  const isCheckGroups = matches.some((m) => m.routeId.startsWith("/orgs/$org/check-groups"));
  const isIncidents = matches.some((m) => m.routeId.startsWith("/orgs/$org/incidents"));
  const isEvents = routeIds.has("/orgs/$org/events");
  const isStatusPages = matches.some((m) => m.routeId.startsWith("/orgs/$org/status-pages"));
  const isStatusUpdates = matches.some((m) => m.routeId.startsWith("/orgs/$org/status-updates"));
  const isChannels = matches.some((m) => m.routeId.startsWith("/orgs/$org/integrations"));
  const isOnCall = matches.some((m) => m.routeId.startsWith("/orgs/$org/on-call"));
  const isEscalation = matches.some((m) => m.routeId.startsWith("/orgs/$org/escalation-policies"));
  const isDependencies = matches.some((m) => m.routeId.startsWith("/orgs/$org/dependencies"));
  const isDesignReference = matches.some((m) => m.routeId.startsWith("/orgs/$org/design-reference"));
  const isDiscovery = matches.some((m) => m.routeId.startsWith("/orgs/$org/discovery"));
  const isJobs = matches.some((m) => m.routeId.startsWith("/orgs/$org/jobs"));
  const isCheckJobDetail = routeIds.has("/orgs/$org/jobs/check/$checkJobUid");
  const isBackgroundJobDetail = routeIds.has("/orgs/$org/jobs/$jobUid");
  const isMaintenance = matches.some((m) =>
    m.routeId.startsWith("/orgs/$org/maintenance-windows"),
  );
  const isMwNew = routeIds.has("/orgs/$org/maintenance-windows/new");
  const isMwDetail = routeIds.has(
    "/orgs/$org/maintenance-windows/$maintenanceWindowUid/",
  );
  const isMwEdit = routeIds.has(
    "/orgs/$org/maintenance-windows/$maintenanceWindowUid/edit",
  );
  const isSlos = matches.some((m) => m.routeId.startsWith("/orgs/$org/slos"));
  const isSlosNew = routeIds.has("/orgs/$org/slos/new");
  const isSlosDetail = routeIds.has("/orgs/$org/slos/$uid/");
  const isSlosEdit = routeIds.has("/orgs/$org/slos/$uid/edit");
  // Organization section — the report-schedules detail/edit page is the same
  // route (no separate /edit), and the layout routes for private-locations
  // and report-schedules resolve their section label via startsWith below.
  const isReportScheduleDetail = routeIds.has(
    "/orgs/$org/organization/report-schedules/$uid",
  );
  const isNotifications = routeIds.has("/orgs/$org/me/notifications");
  const isNotificationDetail = routeIds.has("/orgs/$org/notifications/$notificationUid");

  // Checks section
  const { data: check } = useCheck(org, params.checkUid ?? "");
  // Check-groups section — the route param is `uid`, shared with on-call and
  // escalation-policies, so gate on the section flag like those do.
  const { data: checkGroup } = useCheckGroup(
    org,
    isCheckGroups ? (params.uid ?? "") : "",
  );
  const { data: result } = useResult(
    org,
    params.checkUid ?? "",
    params.resultUid ?? "",
  );
  // Incidents section
  const { data: incident } = useIncident(org, params.incidentUid ?? "");
  // Status pages section
  const { data: statusPage } = useStatusPage(org, params.statusPageUid ?? "");
  // Channels / on-call / escalation-policies sections — short-circuit on
  // empty/wrong-section param so each hook only fetches when its branch is
  // active. on-call and escalation share the param name `uid`, so dispatch
  // by section flag here too.
  const { data: connection } = useIntegration(
    org,
    isChannels ? (params.integrationUid ?? "") : "",
  );
  const { data: onCallSchedule } = useOnCallSchedule(
    org,
    isOnCall ? (params.uid ?? "") : "",
  );
  const { data: escalationPolicy } = useEscalationPolicy(
    org,
    isEscalation ? (params.uid ?? "") : "",
  );
  // Status updates section
  const { data: statusUpdate } = useStatusUpdate(
    org,
    isStatusUpdates ? (params.updateUid ?? "") : "",
  );
  // Jobs section — resolve the active match's `allOrgs` scope so the `Jobs`
  // link round-trips it, then fetch the leaf label only on a detail route.
  const jobsMatch = isJobs
    ? matches.find((m) => m.routeId.startsWith("/orgs/$org/jobs"))
    : undefined;
  const jobsAllOrgs = Boolean(
    (jobsMatch?.search as { allOrgs?: boolean } | undefined)?.allOrgs,
  );
  const { data: checkJob } = useCheckJob(
    org,
    isCheckJobDetail ? (params.checkJobUid ?? "") : "",
    { allOrgs: jobsAllOrgs },
  );
  const { data: backgroundJob } = useBackgroundJob(
    org,
    isBackgroundJobDetail ? (params.jobUid ?? "") : "",
    { allOrgs: jobsAllOrgs },
  );
  // Maintenance windows section — fetch the leaf label only on detail/edit.
  const { data: mw } = useMaintenanceWindow(
    org,
    isMwDetail || isMwEdit ? (params.maintenanceWindowUid ?? "") : "",
  );
  // SLOs section — the route param is `uid`, shared with on-call and
  // escalation-policies, so gate on the section flag like those do; fetch
  // the leaf label only on detail/edit.
  const { data: slo } = useSlo(
    org,
    isSlosDetail || isSlosEdit ? (params.uid ?? "") : "",
  );
  // Organization / report-schedules section — the route param is `uid`,
  // shared with on-call, escalation-policies and SLOs, so gate on the
  // section flag like those do; fetch the leaf label only on detail.
  const { data: reportSchedule } = useReportSchedule(
    org,
    isReportScheduleDetail ? (params.uid ?? "") : "",
  );

  // Notification detail breadcrumb — subscribe to the same query the page uses
  const notifDetailMatch = isNotificationDetail
    ? matches.find((m) => m.routeId === "/orgs/$org/notifications/$notificationUid")
    : undefined;
  const notifFromParam = notifDetailMatch
    ? (notifDetailMatch.search as { from?: string } | undefined)?.from
    : undefined;
  const notifFrom = isNotificationDetail ? parseNotificationFrom(notifFromParam) : null;
  const { data: cachedNotif } = useOrgNotification(
    org,
    isNotificationDetail ? (params.notificationUid ?? "") : "",
  );
  // Fetch the source incident so the link shows the check name (e.g. "Test
  // API"), matching the incident page's own breadcrumb, rather than the
  // incident title ("… is down").
  const { data: notifIncident } = useIncident(
    org,
    notifFrom?.type === "incident" ? notifFrom.uid : "",
  );

  if (isNotificationDetail) {
    const notifLabel = "Notification";
    if (notifFrom?.type === "incident") {
      const incidentLabel =
        notifIncident?.checkName ||
        cachedNotif?.incident?.title ||
        (notifFrom.uid.length >= 8 ? notifFrom.uid.slice(0, 8) : notifFrom.uid);
      return (
        <>
          <Link
            to="/orgs/$org/incidents"
            params={{ org }}
            search={{ state: "all" as const, showSuppressed: undefined }}
            className={linkClass}
          >
            <AlertTriangle className={iconClass} />
            {t("incidents")}
          </Link>
          <BreadcrumbSeparator />
          <Link
            to="/orgs/$org/incidents/$incidentUid"
            params={{ org, incidentUid: notifFrom.uid }}
            className={linkClass}
          >
            {incidentLabel}
          </Link>
          <BreadcrumbSeparator />
          <span className={activeClass}>{notifLabel}</span>
        </>
      );
    }

    if (notifFrom?.type === "integration") {
      const integrationLabel =
        cachedNotif?.connection?.name ||
        (notifFrom.uid.length >= 8 ? notifFrom.uid.slice(0, 8) : notifFrom.uid);
      return (
        <>
          <Link to="/orgs/$org/integrations" params={{ org }} className={linkClass}>
            <Bell className={iconClass} />
            {t("integrations")}
          </Link>
          <BreadcrumbSeparator />
          <Link
            to="/orgs/$org/integrations/$integrationUid"
            params={{ org, integrationUid: notifFrom.uid }}
            className={linkClass}
          >
            {integrationLabel}
          </Link>
          <BreadcrumbSeparator />
          <span className={activeClass}>{notifLabel}</span>
        </>
      );
    }

    // No ?from — show Integrations > Notification as default context
    return (
      <>
        <Link to="/orgs/$org/integrations" params={{ org }} className={linkClass}>
          <Bell className={iconClass} />
          {t("integrations")}
        </Link>
        <BreadcrumbSeparator />
        <span className={activeClass}>{notifLabel}</span>
      </>
    );
  }

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
    const resultUid = params.resultUid;
    const isCheckEdit = routeIds.has("/orgs/$org/checks/$checkUid/edit");
    const isCheckResult = routeIds.has(
      "/orgs/$org/checks/$checkUid/results/$resultUid",
    );
    const isNewCheck = routeIds.has("/orgs/$org/checks/new");
    const checkName = check?.name || check?.slug || checkUid?.slice(0, 8);
    const resultLabel = result?.periodStart
      ? new Date(result.periodStart).toLocaleString()
      : (resultUid?.slice(0, 8) ?? "");

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
            {isCheckEdit || isCheckResult ? (
              <Link to="/orgs/$org/checks/$checkUid" params={{ org, checkUid }} search={{ graphPeriod: undefined, graphFull: undefined, region: undefined }} className={linkClass}>
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
        {isCheckResult && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{resultLabel}</span>
          </>
        )}
      </>
    );
  }

  if (isCheckGroups) {
    // Check groups live inside the checks page; edit is their only route.
    const groupLabel = checkGroup?.name || params.uid?.slice(0, 8);
    return (
      <>
        <Link to="/orgs/$org/checks" params={{ org }} className={linkClass}>
          <ListChecks className={iconClass} />
          {t("checks")}
        </Link>
        <BreadcrumbSeparator />
        <span className={activeClass}>{groupLabel}</span>
        <BreadcrumbSeparator />
        <span className={activeClass}>{t("edit")}</span>
      </>
    );
  }

  if (isIncidents) {
    const incidentUid = params.incidentUid;
    const incidentLabel = incident?.checkName || incidentUid?.slice(0, 8);

    return (
      <>
        {incidentUid ? (
          <Link to="/orgs/$org/incidents" params={{ org }} search={{ state: "all" as const, showSuppressed: undefined }} className={linkClass}><AlertTriangle className={iconClass} />{t("incidents")}</Link>
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
    const isSessions = routeIds.has("/orgs/$org/account/sessions");
    const isSecurity = routeIds.has("/orgs/$org/account/security");
    const isAi = routeIds.has("/orgs/$org/account/mcp");
    const subLabel = isProfile
      ? t("profile")
      : isTokens
        ? t("tokens")
        : isSessions
          ? t("sessions")
          : isSecurity
            ? t("security")
            : isAi
              ? t("ai")
              : null;
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
    let sectionKey: string | null = null;
    for (const [routeId, key] of Object.entries(ORG_SECTION_LABELS)) {
      if (matches.some((m) => m.routeId.startsWith(routeId))) {
        sectionKey = key;
        break;
      }
    }
    const subLabel = sectionKey ? t(sectionKey) : null;

    // Deep leaves — sections with a `.new` / `.$uid` / `.register` child that
    // need a third crumb naming the record, the same shape the SLOs branch
    // uses below.
    const isPrivateLocationsRegister = routeIds.has(
      "/orgs/$org/organization/private-locations/register",
    );
    const isReportScheduleNew = routeIds.has(
      "/orgs/$org/organization/report-schedules/new",
    );
    const hasDeepLeaf =
      isPrivateLocationsRegister || isReportScheduleNew || isReportScheduleDetail;
    const reportScheduleTitle = reportSchedule?.name || params.uid?.slice(0, 8);
    const sectionPath =
      sectionKey === "privateLocations"
        ? "/orgs/$org/organization/private-locations"
        : sectionKey === "reportSchedules"
          ? "/orgs/$org/organization/report-schedules"
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
            {hasDeepLeaf && sectionPath ? (
              <Link to={sectionPath} params={{ org }} className={linkClass}>{subLabel}</Link>
            ) : (
              <span className={activeClass}>{subLabel}</span>
            )}
          </>
        )}
        {isPrivateLocationsRegister && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{t("register")}</span>
          </>
        )}
        {isReportScheduleNew && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{t("new")}</span>
          </>
        )}
        {isReportScheduleDetail && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{reportScheduleTitle}</span>
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

  if (isStatusUpdates) {
    const updateUid = params.updateUid;
    const isNew = routeIds.has("/orgs/$org/status-updates/new");
    const isEdit = routeIds.has("/orgs/$org/status-updates/$updateUid/edit");
    const label = statusUpdate?.title ?? updateUid?.slice(0, 8);

    return (
      <>
        {updateUid || isNew ? (
          <Link to="/orgs/$org/status-updates" params={{ org }} className={linkClass}>
            <Megaphone className={iconClass} />{t("statusUpdates")}
          </Link>
        ) : (
          <span className={activeClass}>
            <Megaphone className={iconClass} />{t("statusUpdates")}
          </span>
        )}
        {isNew && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{t("new")}</span>
          </>
        )}
        {updateUid && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{label}</span>
          </>
        )}
        {isEdit && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{t("edit")}</span>
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

  if (isChannels) {
    const integrationUid = params.integrationUid;
    const isNew = routeIds.has("/orgs/$org/integrations/new");
    const integrationName = connection?.name || integrationUid?.slice(0, 8);

    return (
      <>
        {integrationUid || isNew ? (
          <Link to="/orgs/$org/integrations" params={{ org }} className={linkClass}><Bell className={iconClass} />{t("integrations")}</Link>
        ) : (
          <span className={activeClass}><Bell className={iconClass} />{t("integrations")}</span>
        )}
        {isNew && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{t("new")}</span>
          </>
        )}
        {integrationUid && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{integrationName}</span>
          </>
        )}
      </>
    );
  }

  if (isOnCall) {
    const uid = params.uid;
    const isNew = routeIds.has("/orgs/$org/on-call/new");
    const scheduleName = onCallSchedule?.name || uid;

    return (
      <>
        {uid || isNew ? (
          <Link to="/orgs/$org/on-call" params={{ org }} className={linkClass}><CalendarClock className={iconClass} />{t("onCall")}</Link>
        ) : (
          <span className={activeClass}><CalendarClock className={iconClass} />{t("onCall")}</span>
        )}
        {isNew && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{t("new")}</span>
          </>
        )}
        {uid && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{scheduleName}</span>
          </>
        )}
      </>
    );
  }

  if (isEscalation) {
    const uid = params.uid;
    const isNew = routeIds.has("/orgs/$org/escalation-policies/new");
    const policyName = escalationPolicy?.name || uid;

    return (
      <>
        {uid || isNew ? (
          <Link to="/orgs/$org/escalation-policies" params={{ org }} className={linkClass}><ArrowUpRight className={iconClass} />{t("escalationPolicies")}</Link>
        ) : (
          <span className={activeClass}><ArrowUpRight className={iconClass} />{t("escalationPolicies")}</span>
        )}
        {isNew && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{t("new")}</span>
          </>
        )}
        {uid && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{policyName}</span>
          </>
        )}
      </>
    );
  }

  if (isDependencies) {
    return (
      <span className={activeClass}>
        <GitBranch className={iconClass} />
        {t("dependencies")}
      </span>
    );
  }

  if (isDesignReference) {
    return (
      <span className={activeClass}>
        <Palette className={iconClass} />
        {t("designReference")}
      </span>
    );
  }

  if (isDiscovery) {
    const jobUid = params.jobUid;
    const isNew = routeIds.has("/orgs/$org/discovery/new");
    const isRoot = !jobUid && !isNew;

    return (
      <>
        {isRoot ? (
          <span className={activeClass}><Network className={iconClass} />{t("discovery")}</span>
        ) : (
          <Link to="/orgs/$org/discovery" params={{ org }} className={linkClass}><Network className={iconClass} />{t("discovery")}</Link>
        )}
        {isNew && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{t("new")}</span>
          </>
        )}
        {jobUid && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{jobUid.slice(0, 8)}</span>
          </>
        )}
      </>
    );
  }

  if (isJobs) {
    const isDetail = isCheckJobDetail || isBackgroundJobDetail;
    const leaf = isCheckJobDetail
      ? checkJob?.checkName || params.checkJobUid?.slice(0, 8)
      : isBackgroundJobDetail
        ? backgroundJob?.type || params.jobUid?.slice(0, 8)
        : null;

    return (
      <>
        {isDetail ? (
          <Link
            to="/orgs/$org/jobs"
            params={{ org }}
            search={jobsAllOrgs ? { tab: "jobs", allOrgs: true } : { tab: "jobs" }}
            className={linkClass}
          >
            <Workflow className={iconClass} />
            {t("jobs")}
          </Link>
        ) : (
          <span className={activeClass}>
            <Workflow className={iconClass} />
            {t("jobs")}
          </span>
        )}
        {isDetail && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{leaf}</span>
          </>
        )}
      </>
    );
  }

  if (isMaintenance) {
    const isDetailOrEdit = isMwDetail || isMwEdit;
    const title = mw?.title || params.maintenanceWindowUid?.slice(0, 8);
    return (
      <>
        {isMwNew || isDetailOrEdit ? (
          <Link
            to="/orgs/$org/maintenance-windows"
            params={{ org }}
            className={linkClass}
          >
            <Wrench className={iconClass} />
            {t("maintenanceWindows")}
          </Link>
        ) : (
          <span className={activeClass}>
            <Wrench className={iconClass} />
            {t("maintenanceWindows")}
          </span>
        )}
        {isMwNew && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{t("new")}</span>
          </>
        )}
        {isDetailOrEdit && (
          <>
            <BreadcrumbSeparator />
            {isMwEdit ? (
              <Link
                to="/orgs/$org/maintenance-windows/$maintenanceWindowUid"
                params={{
                  org,
                  maintenanceWindowUid: params.maintenanceWindowUid!,
                }}
                className={linkClass}
              >
                {title}
              </Link>
            ) : (
              <span className={activeClass}>{title}</span>
            )}
          </>
        )}
        {isMwEdit && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{t("edit")}</span>
          </>
        )}
      </>
    );
  }

  if (isSlos) {
    const isDetailOrEdit = isSlosDetail || isSlosEdit;
    const title = slo?.name || params.uid?.slice(0, 8);
    return (
      <>
        {isSlosNew || isDetailOrEdit ? (
          <Link to="/orgs/$org/slos" params={{ org }} className={linkClass}>
            <Target className={iconClass} />
            {t("slos")}
          </Link>
        ) : (
          <span className={activeClass}>
            <Target className={iconClass} />
            {t("slos")}
          </span>
        )}
        {isSlosNew && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{t("new")}</span>
          </>
        )}
        {isDetailOrEdit && (
          <>
            <BreadcrumbSeparator />
            {isSlosEdit ? (
              <Link
                to="/orgs/$org/slos/$uid"
                params={{ org, uid: params.uid! }}
                className={linkClass}
              >
                {title}
              </Link>
            ) : (
              <span className={activeClass}>{title}</span>
            )}
          </>
        )}
        {isSlosEdit && (
          <>
            <BreadcrumbSeparator />
            <span className={activeClass}>{t("edit")}</span>
          </>
        )}
      </>
    );
  }

  if (isNotifications) {
    return (
      <span className={activeClass}>
        <BellRing className={iconClass} />
        {t("myPages")}
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

  // Auto switch-org on cross-org navigation. Org access is enforced by
  // token-scope equality on every surface (REST + live WS): a token minted for
  // org A is denied on org B even for a genuine member (spec 2026-07-15-01). So
  // when a non-super-admin member lands on an org that differs from the token's
  // org, re-mint the session for the URL's org BEFORE any org-scoped request or
  // WS dial fires — otherwise the whole page 403s and the live socket 4403s.
  // Super-admins cross orgs on their claims alone and never need a switch;
  // non-members fall through to the normal 403 ("Permission Denied") handling.
  // orgSwitchFailed holds the slug a switch attempt failed for (so we stop
  // gating and let normal 403 handling take over); switchingForOrgRef dedupes
  // the in-flight switch so the effect fires switchOrg at most once per target.
  const [orgSwitchFailed, setOrgSwitchFailed] = useState<string | null>(null);
  const switchingForOrgRef = useRef<string | null>(null);
  const needsOrgSwitch =
    auth.isAuthenticated &&
    !auth.isLoading &&
    !isLoginPage &&
    auth.org !== null &&
    auth.org !== org &&
    auth.user?.isSuperAdmin !== true &&
    auth.organizations.some((o) => o.slug === org);

  useEffect(() => {
    if (!needsOrgSwitch) {
      switchingForOrgRef.current = null;
      return;
    }
    // Already switching for, or already gave up on, this exact org.
    if (switchingForOrgRef.current === org || orgSwitchFailed === org) return;
    switchingForOrgRef.current = org;
    // On success, auth.org updates and needsOrgSwitch flips false, clearing the
    // gate below. On failure (transient/race), record it so we fall through to
    // normal request handling rather than pinning a loader forever.
    auth.switchOrg(org).catch(() => setOrgSwitchFailed(org));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [needsOrgSwitch, org, orgSwitchFailed]);

  // Handle OAuth callback tokens in URL
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const accessToken = params.get("access_token");
    const oauthOrg = params.get("org") || org;
    const refreshToken = params.get("refresh_token") || undefined;
    const expiresInParam = params.get("expires_in");
    const expiresIn = expiresInParam ? parseInt(expiresInParam, 10) : undefined;

    if (!accessToken) return;

    setOauthProcessing(true);
    auth
      .loginWithOAuth(accessToken, oauthOrg, refreshToken, expiresIn)
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
    return <div className="flex items-center justify-center h-screen"><Loader2 className="h-8 w-8 animate-spin text-muted-foreground" /></div>;
  }

  // Redirect to login once auth finishes loading and user is not authenticated.
  // This covers the case where beforeLoad allowed through while auth was still loading.
  if (auth.isLoading || !auth.isAuthenticated) {
    if (!auth.isLoading) {
      const basepath = import.meta.env.VITE_BASE_URL || "";
      const returnTo = basepath + location.pathname + (location.searchStr || "");
      navigate({
        to: "/orgs/$org/login",
        params: { org },
        search: { session_expired: false, returnTo },
        replace: true,
      });
    }
    return <div className="flex items-center justify-center h-screen"><Loader2 className="h-8 w-8 animate-spin text-muted-foreground" /></div>;
  }

  // Block child rendering (and the LiveEventsProvider's WS dial) until the
  // session token is scoped to the URL's org. Skip the gate if the switch
  // failed — better to render and let per-request 403s surface than to hang.
  if (needsOrgSwitch && orgSwitchFailed !== org) {
    return (
      <div className="flex items-center justify-center h-screen">
        <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
      </div>
    );
  }

  return (
    <LiveEventsProvider org={org}>
    <SidebarProvider defaultOpen={true}>
      <AppSidebar />
      <CommandMenu open={commandMenuOpen} onOpenChange={setCommandMenuOpen} />
      <SidebarInset className="md:ml-0">
        {/* h-12 (48px), not h-16: this bar holds a sidebar toggle, a breadcrumb
            and two icon buttons, and every page repeats the breadcrumb leaf as
            its own <h1> immediately below — 64px of chrome bought nothing but
            lost content height. */}
        <header className="flex h-12 shrink-0 items-center gap-2 border-b px-4">
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
    </LiveEventsProvider>
  );
}
