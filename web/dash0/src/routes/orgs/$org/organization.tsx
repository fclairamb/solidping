import { createFileRoute, Outlet, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/contexts/AuthContext";
import { TabNav } from "@/components/shared/tab-nav";
import { useOrgMembershipRequests } from "@/api/hooks";
import { isOrgDeleted } from "@/api/client";

export const Route = createFileRoute("/orgs/$org/organization")({
  component: OrganizationLayout,
});

function OrganizationLayout() {
  const { t } = useTranslation(["org", "nav"]);
  const { org } = Route.useParams();
  const { user, isLoading } = useAuth();
  const navigate = useNavigate();
  const { data: pendingData } = useOrgMembershipRequests(org, {
    status: "pending",
    enabled: !!user?.isAdmin,
  });
  const pendingCount = pendingData?.data.length ?? 0;

  const tabs = [
    { label: t("nav:members"), path: "/orgs/$org/organization/members" },
    { label: t("nav:invitations"), path: "/orgs/$org/organization/invitations" },
    {
      label: t("nav:requests", "Requests"),
      path: "/orgs/$org/organization/requests",
      badge: pendingCount,
    },
    { label: t("nav:usage", "Usage"), path: "/orgs/$org/organization/usage" },
    {
      label: t("nav:privateLocations"),
      path: "/orgs/$org/organization/private-locations",
    },
    {
      label: t("nav:reportSchedules", "Uptime reports"),
      path: "/orgs/$org/organization/report-schedules",
    },
    { label: t("nav:audit", "Audit"), path: "/orgs/$org/organization/audit" },
    { label: t("nav:settings"), path: "/orgs/$org/organization/settings" },
  ];

  if (isLoading) {
    return null;
  }
  // An owner who just deleted THIS org from the settings tab below is already
  // navigating away — to the surviving org's dashboard, or to /no-org — on a
  // replacement session that carries no admin role here. The guard below would
  // fire on that render and `replace: true` its way onto /orgs/<dead slug>,
  // beating the deliberate navigation and stranding the user on a 404ing org
  // (issue #206). Render nothing for the frame or two until the navigation
  // commits and this layout unmounts.
  if (isOrgDeleted(org)) {
    return null;
  }
  if (!user?.isAdmin) {
    navigate({ to: "/orgs/$org", params: { org }, replace: true });
    return null;
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">{t("org:layout.title")}</h1>
        <p className="text-muted-foreground">{t("org:layout.subtitle")}</p>
      </div>
      <TabNav tabs={tabs} org={org} />
      <Outlet />
    </div>
  );
}
